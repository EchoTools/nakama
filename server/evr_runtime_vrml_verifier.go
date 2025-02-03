package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/echotools/vrmlgo"
	"github.com/go-redis/redis"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

const (
	StorageCollectionVRML            = "VRML"
	StorageKeyVRMLVerificationLedger = "EntitlementLedger"
	StorageIndexVRMLUserID           = "Index_VRMLUserID"
)

type VRMLVerifier struct {
	ctx    context.Context
	logger runtime.Logger
	db     *sql.DB
	nk     runtime.NakamaModule

	redisClient      *redis.Client
	cache            *VRMLCache
	queueKeyPrefix   string
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
		queueKeyPrefix:   "VRMLVerifier:queue:",
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
		return fmt.Errorf("Failed to connect to Redis: %v", err)
	}

	ledger, err := VRMLEntitlementLedgerLoad(v.ctx, v.nk)
	if err != nil {
		return fmt.Errorf("Failed to load VRML entitlement ledger: %v", err)
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
			logger := v.logger.WithFields(map[string]interface{}{
				"user_id": userID,
			})
			vg := v.newSession(token)

			// Get the user identity
			vrmlUser, err := vg.Me()
			if err != nil {
				logger.WithFields(map[string]interface{}{
					"user_id": userID,
					"error":   err,
				}).Error("Failed to get @Me data")
				continue
			}

			// Count the number of matches played by season
			entitlements, err := v.retrieveEntitlements(vg)
			if err != nil {
				logger.WithFields(map[string]interface{}{
					"error":   err,
					"user_id": userID,
				}).Error("Failed to process verification")
				continue
			}

			// Assign the cosmetics to the user
			if err := AssignEntitlements(v.ctx, logger, v.nk, SystemUserID, "", userID, vrmlUser.ID, entitlements); err != nil {
				logger.WithField("error", err).Error("Failed to assign entitlements")
				continue
			}

			// Store the entitlements in the ledger
			ledger.Entries = append(ledger.Entries, &VRMLEntitlementLedgerEntry{
				UserID:       userID,
				VRMLUserID:   vrmlUser.ID,
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

// Enqueue adds an item to the queue.
func (v *VRMLVerifier) enqueue(value string) error {
	return v.redisClient.RPush(v.queueKeyPrefix, value).Err()
}

// Dequeue removes and returns the first item from the queue.
func (v *VRMLVerifier) dequeue() (string, error) {
	val, err := v.redisClient.LPop(v.queueKeyPrefix).Result()
	if err == redis.Nil {
		return "", nil // Queue is empty
	}
	return val, err
}

// BlockingDequeue waits for an item to be available before returning.
func (v *VRMLVerifier) blockingDequeue(timeout time.Duration) (string, error) {
	val, err := v.redisClient.BLPop(timeout, v.queueKeyPrefix).Result()
	if err == redis.Nil {
		return "", nil // No item found within timeout
	}
	if err != nil {
		return "", err
	}
	return val[1], nil
}

func (v *VRMLVerifier) retrieveEntitlements(vg *vrmlgo.Session) ([]*VRMLEntitlement, error) {

	// Fetch the match count by season
	matchCountBySeason, err := FetchMatchCountBySeason(vg)
	if err != nil {
		return nil, fmt.Errorf("Failed to fetch match count by season: %v", err)
	}

	// Validate match counts
	entitlements := make([]*VRMLEntitlement, 0)

	for seasonID, matchCount := range matchCountBySeason {

		switch seasonID {

		// Pre-season and Season 1 have different requirements
		case VRMLPreSeason, VRMLSeason1:
			if matchCount > 0 {
				entitlements = append(entitlements, &VRMLEntitlement{
					SeasonID: seasonID,
				})
			}

		default:
			if matchCount >= 10 {
				entitlements = append(entitlements, &VRMLEntitlement{
					SeasonID: seasonID,
				})
			}
		}
	}

	return entitlements, nil
}
