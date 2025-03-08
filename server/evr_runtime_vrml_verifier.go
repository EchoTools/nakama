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
	"github.com/echotools/vrmlgo/v3"
	"github.com/go-redis/redis"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
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
		return nil, errors.New("Missing VRML_REDIS_URI in server config")
	}

	redisOptions, err := redis.ParseURL(redisUri)
	if err != nil {
		return nil, fmt.Errorf("Failed to parse Redis URI: %v", err)
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
				metadata, err := AccountMetadataLoad(v.ctx, v.nk, userID)
				if err != nil {
					logger.WithField("error", err).Error("Failed to load account metadata")
					continue
				}
				vrmlUserID = metadata.VRMLUserID()
				member, err := vg.Member(vrmlUserID, vrmlgo.WithUseCache(false))
				if err != nil {
					logger.WithField("error", err).Error("Failed to get member data")
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
