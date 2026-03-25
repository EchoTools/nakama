# BUGS_RESOLVED.md

Triage, validation, and proposed fixes for every item in BUGS.md.
Tests live in `server/evr_bugs_test.go`.

---

## Bug 1: IPQS error logging needs backoff

**File:** `server/evr_ip_info_provider_ipqs.go:276`

**Status:** VALIDATED — no backoff exists

**Problem:** `IPQSClient.Get()` logs `"Failed to get IPQS details, failing open."` on every failed request. Under sustained IPQS outage, this floods logs with one warning per login. There is no circuit breaker, exponential backoff, or error-rate limiter. The same applies to the retry path in `IPQSClient.IsVPN()` at line 332.

**Test:** `TestIPQS_NoBackoffOnRepeatedErrors` — confirms IPQSClient has no backoff state fields.

**Proposed Fix:**
Add a circuit breaker to `IPQSClient`:
```go
type IPQSClient struct {
    // ... existing fields ...
    mu                  sync.Mutex
    consecutiveFailures int
    lastFailureTime     time.Time
    backoffDuration     time.Duration // starts at 5s, doubles up to 5min
}
```
In `Get()`, before making a request:
- If `consecutiveFailures > 0` and `time.Since(lastFailureTime) < backoffDuration`, return `nil, nil` (fail open silently, no log).
- On success, reset `consecutiveFailures` and `backoffDuration`.
- On failure, increment `consecutiveFailures`, double `backoffDuration` (capped at 5min), log only on first failure and on state transitions (open→closed, closed→open).

Add a metrics counter `ipqs_circuit_breaker_open` so failures are observable without log spam.

Note: The `retrieve()` function (line 313) also ignores HTTP status codes entirely — a 429 rate limit response gets passed to the JSON decoder, producing decode errors. Fix `retrieve()` to check `resp.StatusCode` before decoding. The IP-API provider (`evr_ip_info_provider_ipapi.go`) has identical issues and should be fixed together.

---

## Bug 2: Need additional IPQS service (ip-api is down)

**Status:** INVESTIGATION — infrastructure/config issue

**Problem:** ip-api is down and there's no active login for IPQS. The `IPInfoCache` tries providers in order (line 50-58 of `evr_ip_info_cache.go`), but if both are down, all IP info lookups fail silently (returns `nil, nil`).

**Proposed Fix:**
1. Obtain IPQS API credentials and configure the `apiKey`.
2. Consider adding a third provider (e.g., MaxMind GeoLite2 local DB) as a fallback that doesn't depend on external APIs.
3. The `IPInfoCache` provider chain already supports multiple providers — just add the new one to the `clients` slice in `NewIPInfoCache`.

---

## Bug 3: Excessive Discord queries on player login

**File:** `server/evr_discord_appbot.go:158`

**Status:** VALIDATED — no guild member caching

**Problem:** Every player login triggers multiple Discord API calls (guild member lookups, role checks, display name updates). With many concurrent logins, this causes Discord rate limiting: `"You are being rate limited."` with `retry_after:316000000` (~5 minutes).

**Proposed Fix:**
Add a TTL cache for Discord guild member data:
```go
type discordMemberCache struct {
    cache *sync.Map // key: "guildID:userID" -> *cachedMember
}
type cachedMember struct {
    member    *discordgo.Member
    fetchedAt time.Time
}
```
- Cache guild member lookups for 5 minutes.
- Batch role checks where possible (Discord's `GuildMembers` endpoint supports bulk queries).
- On rate limit response, respect `retry_after` and queue retries instead of making immediate follow-up calls.

Note: A 5-minute TTL member cache was added in commit `9e0c4095e` (Mar 17 2026) via `evr_discord_integrator.go:35-43`, backed by gateway `GuildMemberUpdate` events. The remaining issue is that `GuildMember()` is still called once per guild during `initializeSession()` (line 655) — a user in 10 guilds triggers 10 API calls. Also, async display name notifications (line 691-697) fire in parallel goroutines with 2 API calls each, without batching.

---

## Bug 4: Game server login session ID is nil

**File:** `server/evr_pipeline.go:510`

**Status:** EXTERNAL — fix is in nevr-runtime, not this codebase

**Problem:** The nevr-runtime game server doesn't populate `loginSessionId` from the game's login response. When the game server (authenticated as `BOT-695081603180789771` via `serverdb_host` discord_id URL param) sends LoginIdentifier messages, the session ID is all zeros, causing the pipeline to reject with "Login session ID is nil."

**Proposed Fix (nevr-runtime):**
nevr-runtime needs to:
1. Read the login session GUID from the game's memory after login.
2. Include it in the ServerDB registration protobuf.
3. Use it for all subsequent LoginIdentifier messages.

**No change needed in this repo** — the nil check at `evr_pipeline.go:509` is correct defensive behavior.

---

## Bug 5: MatchHistory objects persist forever

**Files:** `server/evr_match_label.go:599-615`, `server/evr_runtime_event_remotelogset.go:548,799`

**Status:** VALIDATED — no TTL or cleanup mechanism

**Problem:** `StoreMatchLabel()` writes to the `"MatchHistory"` storage collection on match shutdown/terminate. `DeleteStoredMatchLabel()` is only called during post-match stats processing (`processPostMatchMessages`). If the match ends without stats being reported (server crash, client disconnect, network failure, or non-public match edge cases), the MatchHistory object persists indefinitely. Nakama storage objects have no automatic TTL.

**Test:** `TestMatchHistory_NoTTLOnStorage` — confirms the collection name and documents the absence of cleanup.

**Proposed Fix:**
Add a periodic cleanup goroutine that runs every hour:
```go
func (p *EvrPipeline) cleanupOrphanedMatchHistory(ctx context.Context) {
    ticker := time.NewTicker(1 * time.Hour)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            // List all MatchHistory objects
            // Delete any older than 4 hours (matches shouldn't last longer)
            // Use storage list + filter by create_time
        }
    }
}
```
Register this in the pipeline startup. Alternatively, add a `created_at` field to the stored label and filter during reads.

---

## Bug 6: /search \<ip\> should return full IP info in embed

**File:** `server/evr_discord_appbot_search.go:460-547`

**Status:** VALIDATED — feature gap

**Problem:** The `/search` command builds result embeds showing display name matches, username matches, device matches, and guild matches — but when searching by IP (global operators only), the result embed doesn't include the IP info data (ISP, Organization, VPN status, fraud score, geolocation).

**Proposed Fix:**
In `handleSearch()`, after identifying a result by IP match, load the IP info and add it to the embed:
```go
if canSearchIPs && net.ParseIP(partial) != nil {
    ipInfo, err := d.ipInfoCache.Get(ctx, partial)
    if err == nil && ipInfo != nil {
        embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
            Name: "IP Info (Confidential)",
            Value: fmt.Sprintf("ISP: %s\nOrg: %s\nCity: %s, %s %s\nVPN: %v\nFraud Score: %d",
                ipInfo.ISP(), ipInfo.Organization(),
                ipInfo.City(), ipInfo.Region(), ipInfo.CountryCode(),
                ipInfo.IsVPN(), ipInfo.FraudScore()),
            Inline: false,
        })
    }
}
```
Mark the field as confidential in the embed since it contains PII.

---

## Bug 7: Display name update version check failure (race condition)

**File:** `server/evr_discord_integrator.go:830-831`

**Status:** VALIDATED — no retry on version conflict

**Problem:** `EVRProfileUpdate()` calls `StorableWrite()` which uses optimistic concurrency (version field). When Discord integration and the login flow both read the same EVRProfile simultaneously, the second writer gets a version conflict error: `"Storage write rejected - version check failed."` There is no retry logic.

**Proposed Fix:**
Add retry-on-version-conflict to `EVRProfileUpdate`:
```go
func EVRProfileUpdate(ctx context.Context, nk runtime.NakamaModule, userID string, updateFn func(*EVRProfile)) error {
    for retries := 0; retries < 3; retries++ {
        profile, err := EVRProfileLoad(ctx, nk, userID)
        if err != nil {
            return err
        }
        updateFn(profile)
        err = StorableWrite(ctx, nk, userID, profile)
        if err == nil {
            return nil
        }
        if !isVersionConflict(err) {
            return err
        }
        // Version conflict — reload and retry
    }
    return fmt.Errorf("version conflict after 3 retries")
}
```
This requires refactoring callers to pass an update function instead of a pre-modified profile, ensuring the read-modify-write is atomic per attempt.

Note: A proper retry pattern already exists in the codebase — `SyncJournalAndProfileWithRetry()` in `evr_suspension_profile.go` retries up to 3 times with exponential backoff (10ms, 20ms, 40ms) on version conflicts. The display name path should follow the same pattern.

---

## Bug 8: VPN detection false positives

**Files:** `server/evr_ip_info_provider_ipqs.go:109-111`, `server/evr_ip_info_provider_ipapi.go:60-62`, `server/evr_lobby_joinentrant.go:432`

**Status:** VALIDATED — relates to Bug 17 (SpaceX) and general false-positive rate

**Problem:** VPN detection uses raw boolean flags from IPQS (`VPN`) and IP-API (`Proxy`) with no nuance. The join-entrant check at line 432 blocks users if `BlockVPNUsers && params.isVPN && !IsVPNBypass(userID)`. False positives from satellite ISPs (Starlink), shared connections, and university networks cause legitimate players to be blocked.

**Proposed Fix:**
See Bug 17 for the ISP allowlist. Additionally:
1. Add a fraud score threshold check: only block if `IsVPN() && FraudScore() >= threshold` (the guild already has `FraudScoreThreshold`).
2. Log blocked users with full IP info for audit.
3. Consider a "soft block" mode that warns but doesn't reject.

---

## Bug 9: Extract EVR code to module

**Status:** BACKLOG — architectural refactoring task, not a bug

**Problem:** All EVR-specific code lives directly in the `server` package alongside core Nakama code, making it hard to maintain and test independently.

**Proposed Approach:**
- Identify seams at interface boundaries (e.g., `IPInfoProvider`, `IPInfo`, storage interfaces).
- Extract one component per release, starting with the most self-contained (e.g., IP info providers, statistics queue).
- Create a `server/evr/` sub-package or separate Go module.
- Use dependency injection to decouple from Nakama internals.

---

## Bug 10: BOT- platform IDs processed as real players

**Files:** `server/evr_runtime_event_remotelogset.go:514-534`, `server/evr_pipeline.go:509-511`

**Status:** VALIDATED — no bot filtering in stats processing

**Problem:** Two issues:
1. `processPostMatchMessages()` iterates `statsByPlayer` and processes stats for every XPID including BOT- users from Practice_AI matches. Bot stats are written to leaderboards alongside real player data.
2. `evr_pipeline.go:509` rejects BOT- users with "Login session ID is nil" because they don't have login sessions, but their stats still arrive via remote log events from the game server.

**Test:** `TestRemoteLog_NoBotFiltering` — documents the absence of BOT- prefix filtering.

**Proposed Fix:**
1. Add a helper function:
```go
func IsBotUserID(userID string) bool {
    return strings.HasPrefix(userID, "BOT-")
}
```
2. In `processPostMatchMessages()`, skip bot XPIDs:
```go
for xpid, typeStats := range statsByPlayer {
    playerInfo := label.GetPlayerByEvrID(xpid)
    if playerInfo == nil || IsBotUserID(playerInfo.UserID) {
        continue  // Skip bots
    }
    // ... existing processing ...
}
```
3. For LoggedInUserProfileRequest from BOT- users, return a generated profile with random names and basic loadout instead of rejecting.

Note: Bot platform code is `BOT` (value 5) defined in `evr/core_account_evrid.go:20`. Bots are spawned via `RandomBotEntrantDescriptor()` in `evr/gameserver_session_start.go:239` with `PlatformCode: BOT` and random account ID. The filtering should check `xpid.PlatformCode == evr.BOT` rather than string prefix matching, since that's the canonical way to identify bots. Bot stats currently inflate player ratings and pollute leaderboards.

---

## Bug 11: /show does not show all regions in guild

**File:** `server/evr_discord_appbot_server_embeds.go:46-69`

**Status:** VALIDATED — by design, but missing "show all" mode

**Problem:** `handleShowServerEmbeds()` requires a specific `region` parameter and calls `selectRegionMatches()` which filters to exactly one region. If a guild has servers across multiple regions (us-east, us-west, eu-west), users must run `/show` separately for each region. There's no way to see all regions at once.

**Test:** `TestSelectRegionMatches_SingleRegionOnly` — confirms single-region filtering behavior.

**Proposed Fix:**
Add an "all" option to the region parameter:
```go
if region == "all" || region == "" {
    // Show all regions, grouped by region
    for region, matches := range matchesByRegion {
        // Create a section header for each region
        // Post embeds grouped by region
    }
} else {
    // Existing single-region behavior
}
```

---

## Bug 12: Matchmaker join storage object — is it duplicating next_match_id?

**Status:** NOT A BUG — investigated and clarified

**Problem statement asks:** "what is a Matchmaker join storage object? is it duplicating what matchmakerconfig next_match_id does?"

**Finding:** The `JoinDirective` storage object (in `server/evr_lobby_matchmake.go:385`) IS the implementation of `next_match_id`. The lobby parameters struct has a field `NextMatchID` (line 43) that is loaded from the `JoinDirective`. There is no duplication — `SetNextMatchID()` writes a `JoinDirective` to storage, and the lobby session reads it back via `LoadJoinDirective()`. They are the same thing.

---

## Bug 13: enable_server_embeds_command — is it dead code?

**Status:** NOT DEAD CODE — actively used

**Finding:** `EnableServerEmbedsCommand` is:
- Defined in `server/evr_group_metadata.go:44` as a `bool` field on `GroupMetadata`
- Checked in `server/evr_discord_appbot_handlers.go:138` to gate access to the `/show` command
- If false, returns "The /show command is not enabled for this guild."

**Test:** `TestEnableServerEmbedsCommand_NotDeadCode` — confirms the field exists and is usable.

---

## Bug 14: Other dead service settings?

**Status:** INVESTIGATION NEEDED — requires audit

**Finding:** `GroupMetadata` has many settings. A quick scan shows all major settings are referenced in handler or pipeline code. A full audit would require:
1. Listing all `GroupMetadata` fields
2. Grep for each field's usage across the codebase
3. Identifying any that are only set but never read

This is a maintenance task, not an active bug.

---

## Bug 15: User not found on OtherUserProfileRequest

**File:** `server/evr_pipeline_login.go:1474-1478`

**Status:** EXPECTED BEHAVIOR — severity should be downgraded

**Problem:** When a player views another player's profile in-game, `ServerProfileLoadByXPID` is called with the target's EVR ID. If that XPID has never authenticated through our system (e.g., a player from another server cluster, or a stale XPID), the lookup fails with "user account not found."

**Proposed Fix:**
1. Downgrade the log level from `Error` to `Debug` — this is a normal occurrence, not an error condition:
```go
// Change from:
logger.Error("Failed to load profile from storage", ...)
// To:
logger.Debug("Profile not found for XPID", ...)
```
2. Return a default/anonymous profile instead of nil so the requesting player sees something in-game.

---

## Bug 16: INVALID_PEER_ID research

**File:** `server/evr_runtime_event_remotelogset.go:376`

**Status:** INFORMATIONAL — client-side error, passthrough log

**Problem:** The game client reports `server_address` as `"[INVALID PEER ID]"` in `RemoteLogServerConnectionFailed`. This is a client-side error string meaning the client never received a valid peer ID from matchmaking. Possible causes:
1. Match was cancelled/terminated between assignment and connection
2. Game server went down after matchmaking but before client connected
3. Network partition between client and game server during handshake

**Proposed Fix:**
No server-side code fix needed. This is diagnostic data from the client. To improve observability:
1. Add a counter metric `remotelog_invalid_peer_id_count` to track frequency.
2. Cross-reference with match lifecycle events to determine if matches are being cancelled too aggressively.

---

## Bug 17: SpaceX/Starlink false positive VPN detection

**Files:** `server/evr_ip_info_provider_ipqs.go:109-111`, `server/evr_ip_info_provider_ipapi.go:60-62`

**Status:** VALIDATED — no ISP allowlist for known proxy ISPs

**Problem:** SpaceX/Starlink uses CGNAT and proxy infrastructure. Both IPQS and IP-API flag these addresses as proxy/VPN. `IsVPN()` returns the raw flag with no ISP-based exception. This causes legitimate Starlink users to be blocked when `BlockVPNUsers` is enabled. Additionally, IP-based alt detection is unreliable for CGNAT addresses (many unrelated users share the same IP).

**Test:** `TestIPQS_SpaceXFalsePositiveVPN` — demonstrates IP-API flags SpaceX as VPN and gives 100 fraud score.

**Proposed Fix:**
1. Add a known-proxy ISP allowlist:
```go
var knownProxyISPs = map[string]bool{
    "spacex services": true,
    "starlink":        true,
}

func isKnownProxyISP(isp, org string) bool {
    return knownProxyISPs[strings.ToLower(isp)] ||
           knownProxyISPs[strings.ToLower(org)]
}
```

2. Update `IPQSData.IsVPN()`:
```go
func (r *IPQSData) IsVPN() bool {
    if isKnownProxyISP(r.Response.ISP, r.Response.Organization) {
        return false
    }
    return r.Response.VPN
}
```

3. Update `ipapiData.IsVPN()` and `FraudScore()` similarly.

4. Add an `IsSharedIP() bool` method to the `IPInfo` interface for known CGNAT/proxy ISPs, and use it to skip alt detection for those IPs.

5. Store the shared-IP flag in login data so enforcement can see when alt detection is unreliable for a given player.
