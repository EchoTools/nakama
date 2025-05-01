package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/echotools/vrmlgo/v5"
	"github.com/go-redis/redis"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	StorageCollectionVRML            = "VRML"
	StorageKeyVRMLVerificationLedger = "EntitlementLedger"
	StorageKeyVRMLSummary            = "summary"
	StorageIndexVRMLUserID           = "Index_VRMLUserID"
	DeviceIDPrefixVRML               = "vrml:"
)

type VRMLVerifier struct {
	ctx    context.Context
	logger runtime.Logger
	db     *sql.DB
	nk     runtime.NakamaModule

	redisClient      *redis.Client
	cache            *VRMLCache
	queueKey         string
	oauthRedirectURL string
	oauthClientID    string
}

func NewVRMLVerifier(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer, appBot *discordgo.Session) (*VRMLVerifier, error) {
	vars := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)

	// Connect to the redis server for the queue and cache
	redisUri := vars["VRML_REDIS_URI"]
	if redisUri == "" {
		return nil, errors.New("missing VRML_REDIS_URI in server config")
	}

	redisOptions, err := redis.ParseURL(redisUri)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Redis URI: %v", err)
	}

	redisClient := redis.NewClient(redisOptions)

	ctx = context.Background()

	// Configure the client with the redis cache
	verifier := &VRMLVerifier{
		ctx:    ctx,
		logger: logger,
		db:     db,
		nk:     nk,

		redisClient:      redisClient,
		cache:            NewVRMLCache(redisClient, "VRMLCache:cache:"),
		queueKey:         "VRMLVerifier:queue",
		oauthRedirectURL: vars["VRML_OAUTH_REDIRECT_URL"],
		oauthClientID:    vars["VRML_OAUTH_CLIENT_ID"],
	}

	// Register the RPC function
	if err = initializer.RegisterRpc("oauth/vrml_redirect", verifier.RedirectRPC); err != nil {
		return nil, errors.New("unable to register rpc")
	}

	// Register the storage index
	if err := initializer.RegisterStorageIndex(
		StorageIndexVRMLUserID,
		StorageCollectionSocial,
		StorageKeyVRMLUser,
		[]string{"userID"},
		nil,
		1000000,
		false,
	); err != nil {
		return nil, err
	}

	verifier.Start()

	return verifier, nil
}
func (v *VRMLVerifier) Start() error {

	_, err := v.redisClient.Ping().Result()
	if err != nil {
		return fmt.Errorf("failed to connect to Redis: %v", err)
	}

	ledger, err := VRMLEntitlementLedgerLoad(v.ctx, v.nk)
	if err != nil {
		return fmt.Errorf("failed to load VRML entitlement ledger: %v", err)
	}

	go func() {

		for {
			select {
			case <-v.ctx.Done():
				return
			case <-time.After(1 * time.Second):
			}

			val, err := v.dequeue()
			if err != nil {
				v.logger.Error("Failed to dequeue item", zap.Error(err))
				continue
			}

			if val == "" {
				continue
			}

			userID, token, found := strings.Cut(val, ":")
			if !found {
				v.logger.Error("Invalid queue item", zap.String("item", val))
				continue
			}
			logger := v.logger.WithFields(map[string]any{
				"user_id": userID,
			})

			var (
				vg         = v.newSession(token)
				vrmlUserID string
			)

			if token == "" {
				// Get the user's VRML ID from their account metadata
				metadata, err := EVRProfileLoad(v.ctx, v.nk, userID)
				if err != nil {
					logger.WithField("error", err).Error("Failed to load account metadata")
					continue
				}
				vrmlUserID = metadata.VRMLUserID()
				member, err := vg.Member(vrmlUserID, vrmlgo.WithUseCache(false))
				if err != nil {
					logger.WithField("error", err).Error("Failed to get member data")
					continue
				}
				vrmlUserID = member.User.ID

			} else {
				// Get the vrmlUserID from the token
				vrmlUser, err := vg.Me(vrmlgo.WithUseCache(false))
				if err != nil {
					logger.WithField("error", err).Error("Failed to get @Me data")
					continue
				}
				vrmlUserID = vrmlUser.ID
			}

			// Get the player summary
			summary, err := v.playerSummary(vg, vrmlUserID)
			if err != nil {
				logger.WithFields(map[string]any{
					"error": err,
				}).Error("Failed to get player summary")
				continue
			}

			// Store the summary
			data, err := json.Marshal(summary)
			if err != nil {
				logger.WithFields(map[string]any{
					"error": err,
				}).Error("Failed to marshal player summary")
				continue
			}

			// Store the VRML user data in the database (effectively linking the account)
			if _, err := v.nk.StorageWrite(v.ctx, []*runtime.StorageWrite{
				{
					Collection:      StorageCollectionVRML,
					Key:             StorageKeyVRMLSummary,
					UserID:          userID,
					Value:           string(data),
					PermissionRead:  1,
					PermissionWrite: 0,
				},
			}); err != nil {
				logger.WithFields(map[string]any{
					"error": err,
				}).Error("Failed to store user")
				continue
			}
			// Count the number of matches played by season
			entitlements := summary.Entitlements()

			// Assign the cosmetics to the user
			if err := AssignEntitlements(v.ctx, logger, v.nk, SystemUserID, "", userID, vrmlUserID, entitlements); err != nil {
				logger.WithField("error", err).Error("Failed to assign entitlements")
				continue
			}

			// Store the entitlements in the ledger
			ledger.Entries = append(ledger.Entries, &VRMLEntitlementLedgerEntry{
				UserID:       userID,
				VRMLUserID:   vrmlUserID,
				Entitlements: entitlements,
			})

			if err := VRMLEntitlementLedgerStore(v.ctx, v.nk, ledger); err != nil {
				logger.WithField("error", err).Error("Failed to store ledger")
				continue
			}
		}

	}()

	return nil
}

func (v *VRMLVerifier) newSession(token string) *vrmlgo.Session {
	vg := vrmlgo.New(token)
	vg.Cache = v.cache
	vg.CacheEnabled = true
	return vg
}

func (v *VRMLVerifier) VerifyUser(userID string, token string) error {
	// Send it to the queue channel
	return v.enqueue(fmt.Sprintf("%s:%s", userID, token))
}

func (v *VRMLVerifier) enqueue(value string) error {
	return v.redisClient.RPush(v.queueKey, value).Err()
}

func (v *VRMLVerifier) dequeue() (string, error) {
	val, err := v.redisClient.LPop(v.queueKey).Result()
	if err == redis.Nil {
		return "", nil // Queue is empty
	}
	return val, err
}

func GetVRMLAccountOwner(ctx context.Context, nk runtime.NakamaModule, vrmlUserID string) (string, error) {
	// Check if the account is already owned by another user
	objs, _, err := nk.StorageIndexList(ctx, SystemUserID, StorageIndexVRMLUserID, fmt.Sprintf("+value.userID:%s", vrmlUserID), 100, nil, "")
	if err != nil {
		return "", fmt.Errorf("error checking ownership: %w", err)
	}

	if len(objs.Objects) == 0 {
		return "", nil
	}

	return objs.Objects[0].UserId, nil
}

func (d *DiscordAppBot) handleVRMLVerify(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
	// accountLinkCommandHandler handles the account link command from Discord

	var (
		nk             = d.nk
		db             = d.db
		vrmlUser       *vrmlgo.User
		editResponseFn = func(content string) error {
			_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
				Content: &content,
			})
			return err
		}
	)

	// Try to load the user's existing VRML account data
	if account, err := nk.AccountGetId(ctx, userID); err != nil {
		return fmt.Errorf("failed to get account: %w", err)
	} else {

		// Search the user's devices for a VRML userID
		for _, d := range account.Devices {
			if vrmlUserID, found := strings.CutPrefix(d.Id, DeviceIDPrefixVRML); found {
				vg := vrmlgo.New("")
				if m, err := vg.Member(vrmlUserID); err != nil {
					return fmt.Errorf("failed to get VRML user: %w", err)
				} else {
					vrmlUser = m.User
				}
				break
			}
		}
	}

	go func() {

		if vrmlUser == nil {

			vars, _ := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)

			// Start the OAuth flow
			timeoutDuration := 5 * time.Minute
			flow, err := NewVRMLOAuthFlow(vars["VRML_OAUTH_CLIENT_ID"], vars["VRML_OAUTH_REDIRECT_URL"], timeoutDuration)
			if err != nil {
				logger.Error("Failed to start OAuth flow", zap.Error(err))
				return
			}

			// Send the link to the user
			if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags:   discordgo.MessageFlagsEphemeral | discordgo.MessageFlagsSuppressEmbeds,
					Content: fmt.Sprintf("To assign your cosmetics, [Verify your VRML account](%s)", flow.url),
				},
			}); err != nil {
				logger.Error("Failed to send interaction response", zap.Error(err))
				return
			}

			// Wait for the token to be returned
			select {
			case <-time.After(timeoutDuration):
				simpleInteractionResponse(s, i, "OAuth flow timed out. Please run the command again.")
				return // Timeout
			case token := <-flow.tokenCh:
				logger := logger.WithFields(map[string]any{
					"user_id":    userID,
					"discord_id": i.Member.User.ID,
				})

				vg := vrmlgo.New(token)

				vrmlUser, err = vg.Me(vrmlgo.WithUseCache(false))
				if err != nil {
					logger.Error("Failed to get VRML user data")
					return
				}
				logger = logger.WithFields(map[string]any{
					"vrml_id":         vrmlUser.ID,
					"vrml_discord_id": vrmlUser.GetDiscordID(),
				})

				if vrmlUser.GetDiscordID() != i.Member.User.ID {
					logger.Error("Discord ID mismatch")
					vrmlLink := fmt.Sprintf("[%s](https://vrmasterleague.com/EchoArena/Users/%s)", vrmlUser.UserName, vrmlUser.ID)
					thisDiscordTag := fmt.Sprintf("%s#%s", user.Username, user.Discriminator)
					editResponseFn(fmt.Sprintf("VRML account %s is currently linked to %s. Please relink the VRML account to this Discord account (%s).", vrmlLink, vrmlUser.DiscordTag, thisDiscordTag))
					return
				}
			}
		} else {

			// Send the link to the user
			if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags:   discordgo.MessageFlagsEphemeral | discordgo.MessageFlagsSuppressEmbeds,
					Content: fmt.Sprintf("Your VRML account (`%s`) is already linked. Reverifying your entitlements...", vrmlUser.UserName),
				},
			}); err != nil {
				logger.Error("Failed to send interaction response", zap.Error(err))
				return
			}
		}

		// Check if the account is already owned by another user
		ownerID, err := GetUserIDByDeviceID(ctx, db, DeviceIDPrefixVRML+vrmlUser.ID)
		if err != nil && status.Code(err) != codes.NotFound {
			logger.Error("Failed to check current owner")
		} else if ownerID != "" && ownerID != userID {
			content := fmt.Sprintf("Account already owned by another user. [Contact EchoVRCE](%s) if you need to unlink it.", ServiceSettings().ReportURL)
			logger.WithField("owner_id", ownerID).Error(content)
			editResponseFn("Account already owned by another user")
			return
		}

		// Link the accounts
		if err := LinkVRMLAccount(ctx, nk, userID, vrmlUser); err != nil {
			if err := editResponseFn("Failed to link accounts"); err != nil {
				logger.Error("Failed to edit response", zap.Error(err))
			}
			logger.Error("Failed to link accounts", zap.Error(err))
			return
		}

		if err := editResponseFn(fmt.Sprintf("Your VRML account (`%s`) has been verified/linked. It will take a few minutes--up to a few hours--to update your entitlements.", vrmlUser.UserName)); err != nil {
			logger.Error("Failed to edit response", zap.Error(err))
			return
		}

	}()

	return nil
}
