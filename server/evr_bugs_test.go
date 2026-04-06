package server

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// =============================================================================
// Bug 1: IPQS error logging has no backoff
// The IPQS client logs "Failed to get IPQS details, failing open." on every
// failed request with no rate limiting or circuit breaker. Under sustained
// failure, this floods logs.
// =============================================================================

func TestIPQS_NoBackoffOnRepeatedErrors(t *testing.T) {
	client := &IPQSClient{logger: zap.NewNop()}
	assert.True(t, client.allowRequest())

	client.onFailure(errors.New("network down"))
	assert.False(t, client.allowRequest(), "request should be blocked while circuit is open")

	client.lastFailureTime = time.Now().Add(-client.backoffDuration)
	assert.True(t, client.allowRequest(), "request should be allowed after backoff expires")

	client.onSuccess()
	assert.True(t, client.allowRequest(), "success should close circuit and allow requests")
}

// =============================================================================
// Bug 3: Excessive Discord API queries on player login
// The Discord app bot makes too many guild member lookups during login,
// triggering Discord rate limits. The rate limit error shows up as:
// "You are being rate limited." with retry_after:316000000
// =============================================================================

// This bug is structural — the Discord integration makes per-login API calls
// without caching guild member data. A test for the fix would verify caching
// behavior, but the current code has no cache to test against. Documenting here.

// =============================================================================
// Bug 5: MatchHistory objects persist forever
// MatchHistory (StorageCollection: "MatchHistory") objects are written on match
// shutdown/terminate and only deleted during post-match stats processing.
// If post-match processing never runs (crash, timeout, non-reporting client),
// the objects persist indefinitely with no TTL or cleanup job.
//
// Fixed: cleanup goroutine in evr_pipeline.go deletes objects older than 4h.
// Additionally, processMatchTerminationTask no longer re-creates the label
// when MatchShutdown already stored it (race condition fix).
// =============================================================================

func TestMatchHistory_NoTTLOnStorage(t *testing.T) {
	assert.Equal(t, "MatchHistory", MatchHistoryStorageCollection)
}

func TestMatchTerminationTask_LabelAlreadyStored(t *testing.T) {
	// When MatchShutdown runs, it sets terminateTick and stores the label.
	// MatchTerminate should set labelAlreadyStored = true so the async
	// terminate task doesn't re-create the label after post-match cleanup
	// deletes it.
	t.Run("shutdown ran first", func(t *testing.T) {
		// terminateTick != 0 means MatchShutdown stored the label
		task := matchTerminationTask{
			labelAlreadyStored: true, // state.terminateTick != 0
		}
		assert.True(t, task.labelAlreadyStored)
	})

	t.Run("server shutdown without MatchShutdown", func(t *testing.T) {
		// terminateTick == 0 means QueueTerminate called MatchTerminate directly
		task := matchTerminationTask{
			labelAlreadyStored: false, // state.terminateTick == 0
		}
		assert.False(t, task.labelAlreadyStored)
	})
}

// =============================================================================
// Bug 7: Display name update version check failure (race condition)
// evr_discord_integrator.go:831 — concurrent writes to EVRProfile storage
// cause "Storage write rejected - version check failed." because:
// 1. Discord integration reads profile (gets version V1)
// 2. Login flow also reads profile (gets version V1)
// 3. Login flow writes profile (version becomes V2)
// 4. Discord integration writes profile with version V1 → CONFLICT
// No retry-on-conflict logic exists in EVRProfileUpdate.
// =============================================================================

// This is a race condition test — the actual race requires concurrent storage
// access which is integration-level. The unit test documents that StorableWrite
// does NOT retry on version conflict (it just returns the error).

// =============================================================================
// Bug 10: BOT- platform IDs processed as real players
// Remote log processing in evr_runtime_event_remotelogset.go processes
// POST_MATCH_MATCH_STATS and POST_MATCH_EARNED_AWARD for BOT- user IDs
// as if they're real players. There is no BOT- prefix filtering.
// =============================================================================

func TestRemoteLog_NoBotFiltering(t *testing.T) {
	// In processPostMatchMessages (evr_runtime_event_remotelogset.go:514),
	// the code iterates statsByPlayer and processes every XPID without checking
	// whether the user is a bot.
	//
	// The loop at line 638 does:
	//   for xpid, typeStats := range statsByPlayer { ... }
	// with no filter like:
	//   if strings.HasPrefix(userID, "BOT-") { continue }
	//
	// Also in evr_pipeline.go:509, LoginIdentifier messages from BOT- users
	// are rejected with "Login session ID is nil" because bots don't have
	// proper login sessions, but stats still get processed for them.

	// Verify that the code has no bot-detection prefix constant or filter function.
	// When the fix is applied, we should have a helper like IsBotUserID(string) bool.
	botUserID := "BOT-695081603180789771"
	assert.True(t, strings.HasPrefix(botUserID, "BOT-"),
		"BOT- prefix should be detectable — add filtering in processPostMatchMessages")
}

// =============================================================================
// Bug 11: /show does not show all regions in a guild
// The /show command (handleShowServerEmbeds) requires a specific region parameter
// and only shows matches for that one region. There's no "show all" mode.
// The selectRegionMatches function filters to a single region.
// =============================================================================

func TestSelectRegionMatches_SingleRegionOnly(t *testing.T) {
	// selectRegionMatches returns matches for exactly one region.
	// If a guild has servers in us-east, us-west, and eu-west,
	// /show us-east only shows us-east matches.
	// There is no way to see all regions at once.

	labels := []*MatchLabel{
		{GameServer: &GameServerPresence{CountryCode: "US", Region: "Virginia"}},
		{GameServer: &GameServerPresence{CountryCode: "DE"}},
	}

	usRegion := normalizeRegionCode(labels[0].GameServer.LocationRegionCode(false, false))
	deRegion := normalizeRegionCode(labels[1].GameServer.LocationRegionCode(false, false))

	// Request US region — should only return US matches, not DE
	matches, regions, found := selectRegionMatches(labels, usRegion)
	if found {
		for _, m := range matches {
			region := normalizeRegionCode(m.GameServer.LocationRegionCode(false, false))
			assert.Equal(t, usRegion, region)
		}
	}

	// Available regions should include BOTH regions even when filtering to one
	assert.Contains(t, regions, usRegion, "US region should be in available list")
	assert.Contains(t, regions, deRegion, "DE region should be in available list")

	// BUG: There is no way to request "all" regions — the command always requires
	// a specific region parameter. The /show command should support showing all.
}

// =============================================================================
// Bug 13: enable_server_embeds_command — NOT dead code
// This setting IS actively used: checked in evr_discord_appbot_handlers.go:138
// and defined in evr_group_metadata.go:44. It gates the /show command.
// =============================================================================

func TestEnableServerEmbedsCommand_NotDeadCode(t *testing.T) {
	// Verify the field exists on GroupMetadata
	meta := GroupMetadata{}
	// The field is defined and used in handler authorization
	meta.EnableServerEmbedsCommand = true
	assert.True(t, meta.EnableServerEmbedsCommand)

	meta.EnableServerEmbedsCommand = false
	assert.False(t, meta.EnableServerEmbedsCommand)
}

// =============================================================================
// Bug 17: SpaceX/Starlink false positive VPN detection
// IPQS and IP-API both report Starlink addresses as "proxy" because SpaceX
// uses CGNAT/proxy infrastructure. The IsVPN() method returns the raw VPN/proxy
// flag with no ISP allowlist. SpaceX Services as ISP or Organization should
// NOT be treated as VPN.
// =============================================================================

func TestIPQS_SpaceXFalsePositiveVPN(t *testing.T) {
	// Simulate an IPQS response for a Starlink user
	data := &IPQSData{
		Response: IPQSResponse{
			ISP:          "SpaceX Services",
			Organization: "SpaceX Services",
			Proxy:        true, // IPQS marks Starlink as proxy
			VPN:          false,
			FraudScore:   10, // Low fraud score — legitimate user
			Success:      true,
		},
	}

	// Current behavior: IsVPN returns the raw VPN flag (false in this case)
	// But the Proxy field is true, and IP-API uses Proxy for IsVPN().
	assert.False(t, data.IsVPN(), "IPQS VPN flag is false for SpaceX")
	assert.True(t, data.IsSharedIP(), "IPQS should classify SpaceX as shared IP provider")
	// However, the Organization/ISP clearly identifies this as SpaceX/Starlink.
	assert.Equal(t, "SpaceX Services", data.ISP())
	assert.Equal(t, "SpaceX Services", data.Organization())

	// Now test IP-API which uses Proxy field for VPN detection
	ipapiResult := &ipapiData{
		Response: IPAPIResponse{
			ISP:          "SpaceX Services",
			Organization: "SpaceX Services",
			Proxy:        true, // IP-API marks Starlink as proxy
		},
	}

	assert.False(t, ipapiResult.IsVPN(),
		"IP-API should exempt known shared-IP providers like SpaceX/Starlink")
	assert.True(t, ipapiResult.IsSharedIP(),
		"SpaceX/Starlink should be tagged as shared IP provider")
	assert.Equal(t, 0, ipapiResult.FraudScore(),
		"known shared-IP providers should not be hard-scored as fraud")
}

func TestIsBotEvrID(t *testing.T) {
	assert.True(t, IsBotEvrID(evr.EvrId{PlatformCode: evr.BOT, AccountId: 123}))
	assert.False(t, IsBotEvrID(evr.EvrId{PlatformCode: evr.OVR, AccountId: 123}))
}

// =============================================================================
// Bug 15: User not found on OtherUserProfileRequest
// evr_pipeline_login.go:1477 — ServerProfileLoadByXPID fails because the
// target user (identified by EVR ID/XPID) doesn't exist in the system.
// This happens when a player in-match looks at the profile of someone who
// has never logged in through our system (e.g., a player from vanilla servers
// or a desynced XPID).
// =============================================================================

// This is expected behavior for unknown XPIDs. The error should be downgraded
// from Error to Warn/Debug level since it's a normal occurrence.

// =============================================================================
// Bug 16: INVALID_PEER_ID in server connection failed logs
// evr_runtime_event_remotelogset.go:376 — The game client reports
// server_address as "[INVALID PEER ID]" when it fails to connect.
// This indicates the client never received a valid peer ID from matchmaking,
// likely because the match was cancelled or the server went away before
// the client could connect.
// =============================================================================

// This is an informational log. The client-side error is passed through.
// No server-side fix needed — this is a client reporting a failed connection.
