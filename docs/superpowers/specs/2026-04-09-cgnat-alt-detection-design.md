# CGNAT-Aware Alt Detection

## Problem

The alt account detection system links accounts based on shared login signals: IP address, HMD serial number, XPID, and system profile. Any **single** matching signal is sufficient to create an alt link (`loginHistoryCompare` in `evr_authenticate_alts.go:125`).

CGNAT systems (Starlink AS14593, mobile carriers, university networks, corporate VPNs) cause many unrelated players to share a public IP address. This creates massive false-positive alt clusters — 30+ players linked via a shared Starlink IP like 129.222.210.x. Enforcement cascades (suspension kicks via `KickPlayersWithDisabledAlternates`, lobby rejections via `RejectPlayersWithSuspendedAlternates`) then harm innocent players.

Quest headsets compound this: their `SystemProfile()` values are nearly identical across devices of the same model, creating a second class of weak signal that produces false positives.

## Solution

A three-layer CGNAT detection system that identifies shared-IP providers and excludes their IPs from alt matching. Commodity hardware profiles are also filtered. Defense in depth: write-time filtering prevents new bad links, read-time enforcement guards neutralize existing bad links immediately, and retroactive cleanup removes them permanently.

## Signal Classification

The core concept: not all matching signals are equally reliable.

| Signal                    | Strength   | Rationale                                      |
| ------------------------- | ---------- | ---------------------------------------------- |
| HMD Serial Number         | **Strong** | Hardware-bound, unique per device              |
| XPID                      | **Strong** | Platform-bound user identity                   |
| Client IP (non-CGNAT)     | **Strong** | Residential IPs typically identify a household |
| Client IP (CGNAT)         | **Weak**   | Shared by hundreds of unrelated users          |
| SystemProfile (unique)    | **Strong** | Distinctive hardware combinations              |
| SystemProfile (commodity) | **Weak**   | Identical across all Quest 3s, Quest 2s, etc.  |

An alt link requires at least one **strong** signal. Links based entirely on weak signals are:

- Not created (write-time filter)
- Not enforced (read-time guard)
- Removed retroactively (cleanup)

## Architecture

### CGNATDetector

A singleton initialized at startup, accessed via `go.uber.org/atomic` pointer (matching the existing `serviceSettings` pattern in `evr_global_settings.go`). Three detection layers:

**Layer 1 — ASN Lookup:** Downloads both ip2asn-v4 and ip2asn-v6 TSV data from iptoasn.com (open, no account needed). Parses IPv4 ranges (~600k) into `[]asnRange4` (uint32 binary search) and IPv6 ranges (~150k) into `[]asnRange6` ([16]byte binary search). `IsCGNAT` dispatches to the correct dataset based on `net.IP.To4()`. Checks resolved ASN against a configurable list of CGNAT ASNs in settings. Loaded in a background goroutine — does not block startup.

**Layer 2 — CIDR Range List:** Configurable list of CIDR strings in settings (seeded with RFC 6598 `100.64.0.0/10`). Parsed into `[]*net.IPNet` on settings change. Available immediately at startup.

**Layer 3 — Heuristic (disabled by default):** Tracks unique account count per IP in memory. When a configurable threshold is exceeded within a configurable time window, warns moderators via audit channel. Does NOT auto-exempt — moderators decide whether to add the range to the CIDR list.

### Key Functions

```go
// IsCGNAT returns true if the IP belongs to a known CGNAT system.
// Handles both IPv4 and IPv6. Returns false for unparseable input.
// Checks: CIDR list (fastest), then ASN lookup (v4 or v6 dataset).
// Heuristic does NOT affect return value.
func (d *CGNATDetector) IsCGNAT(ip string) bool

// IsWeakSignal returns true if the item is a CGNAT IP or commodity profile.
func (d *CGNATDetector) IsWeakSignal(item string) bool

// TrackLogin records a login for heuristic tracking. Warns moderators
// if threshold exceeded. Prunes stale entries.
func (d *CGNATDetector) TrackLogin(ip, userID, auditChannelID string, dg DiscordGoClient)

// filterStrongAlts returns only alt IDs that have at least one strong-signal
// match. Iterates history.AlternateMatches[altID] and checks each match's
// Items list via detector.IsWeakSignal(). Excludes alts where ALL items
// across ALL matches are weak signals.
func filterStrongAlts(history *LoginHistory, altIDs []string, detector *CGNATDetector) []string
```

### Settings

Added to `ServiceSettingsData`:

```go
CGNAT CGNATSettings `json:"cgnat"`

type CGNATSettings struct {
    ASNs                     []int    `json:"asns"`                       // CGNAT ASNs, e.g. [14593]
    CIDRs                    []string `json:"cidrs"`                      // CGNAT CIDRs, e.g. ["100.64.0.0/10"]
    CommodityProfilePrefixes []string `json:"commodity_profile_prefixes"` // e.g. ["Quest 3::"]
    HeuristicEnabled         bool     `json:"heuristic_enabled"`          // default: false
    HeuristicAccountThreshold int     `json:"heuristic_threshold"`        // accounts per IP
    HeuristicWindowDays      int      `json:"heuristic_window_days"`      // time window
}
```

Hot-reloadable via the settings RPC — takes effect on next login.

## Integration Points

### 0. Bug Fix: `otherHistories` map never populated

`LoginAlternatePatternSearch` (`evr_authenticate_alts.go:58-97`) allocates `otherHistories` but never inserts into it. `UpdateAlternates` at `evr_authenticate_history.go:441` can never populate second-degree alternates. Fix: add `otherHistories[obj.UserId] = otherHistory` after line 87. Required for cleanup's "clear and recompute" strategy.

### 1. Write-time filter: `matchIgnoredAltPattern()`

**File:** `evr_authenticate_history.go:47`

Extend to call `IsCGNAT()` for IP patterns (alongside existing `IsPrivate()` check) and check commodity profile prefixes. Filtered items don't enter `LoginHistory.Cache`, so `LoginAlternateSearch` won't find them. This function is called from both `rebuildCache()` (line 563) and `Patterns()` (line 94), so both paths are covered by the single change.

### 2. Read-time enforcement guard

**File:** `evr_pipeline_login.go:521-522`

Filter `enforcementUserIDs` to exclude alts whose links are based entirely on weak signals. Provides **immediate protection on deployment** — enforcement stops cascading through CGNAT-only links before cleanup runs.

Also applied at `evr_runtime_event_user_authenticate.go:78`: between the `UpdateAlternates` call (line 69) and the `hasDiabledAlts` check (line 78), re-check `loginHistory.AlternateMatches` via `filterStrongAlts`. If no strong-signal disabled alts remain, set `hasDiabledAlts = false` to prevent the kick path from firing.

### 3. Heuristic tracking

**File:** `evr_runtime_event_user_authenticate.go` — after `loginHistory.Update()`.

### 4. Startup initialization

**File:** `evr_runtime.go` — create detector, update settings (CIDR immediate), background goroutine for ASN download + retroactive cleanup.

### 5. Cleanup RPC

**File:** `evr_runtime_rpc_cgnat_cleanup.go` — `cgnat/cleanup` endpoint (Global Operators only).

## Retroactive Cleanup

Scans all `LoginHistory` records via `nk.StorageList(ctx, callerID, "", LoginStorageCollection, limit, cursor)` — pass empty `userID` for system-level scan. Process in batches of 100. For each alt link, removes weak-signal items. If no strong signals remain, breaks the link bidirectionally. Clears `SecondDegreeAlternates` for affected users (recomputed on next login).

Uses unconditional writes (`version: ""`) — safe because cleanup only removes data, and concurrent logins will re-evaluate with the CGNAT filter active.

Runs on startup (background) and on-demand via RPC. Idempotent. Posts audit summary to Discord.

## Relationship to Existing Systems

`isKnownSharedIPProvider()` in `evr_ip_info_shared.go` identifies Starlink by ISP/org name via external API (IPQS/ip-api) for VPN/fraud scoring exemption. The `CGNATDetector` operates on raw IPs without external API calls, at the alt detection layer. Complementary, not overlapping.

## Guardrails

1. **No false negatives.** Strong signals (HMD serial, XPID) always create alt links regardless of IP status.
2. **No silent data loss.** Every broken link logged to audit channel.
3. **No enforcement on weak-only links.** Enforced at both write time and read time (defense in depth).
4. **Graceful degradation.** ASN failure → CIDR + heuristic still work. All layers empty → no regression.
5. **No auto-exemption from heuristic.** Heuristic warns only; moderators act.
6. **Unconditional writes for cleanup.** Avoids version conflicts in bulk operations.
7. **Hot-reloadable settings.** Changes take effect on next login.
8. **Non-blocking startup.** ASN loads in background; CIDR available immediately.
9. **Full IPv6 support.** Both IPv4 and IPv6 handled by all layers (ASN, CIDR, heuristic). Unparseable addresses return false.
10. **Testable.** Constructor injection; tests use own instances.

## Files

| File                                     | Action | Purpose                                                |
| ---------------------------------------- | ------ | ------------------------------------------------------ |
| `evr_authenticate_alts.go`               | Modify | Fix `otherHistories` bug                               |
| `evr_cgnat.go`                           | Create | CGNATDetector, IsCGNAT, IsWeakSignal, filterStrongAlts |
| `evr_cgnat_test.go`                      | Create | 59 tests                                               |
| `evr_global_settings.go`                 | Modify | CGNATSettings struct                                   |
| `evr_authenticate_history.go`            | Modify | matchIgnoredAltPattern: CGNAT + commodity profile      |
| `evr_pipeline_login.go`                  | Modify | Enforcement-time guard                                 |
| `evr_runtime_event_user_authenticate.go` | Modify | Heuristic tracking + kick validation                   |
| `evr_runtime_rpc_cgnat_cleanup.go`       | Create | Cleanup RPC + startup cleanup                          |
| `evr_runtime_rpc_registration.go`        | Modify | Register `cgnat/cleanup` in `RegisterEVRRPCs`          |
| `evr_runtime.go`                         | Modify | Initialize detector, background tasks                  |

## Production Test Fixtures

Real cases extracted from production database (2026-04-09). Fixture file: `server/testdata/cgnat_test_cases.json`.

**3,203 users** have at least one alt link based entirely on weak signals.

| Case                      | Users               | Signal                                        | ASN     | Match Count | Expected     |
| ------------------------- | ------------------- | --------------------------------------------- | ------- | ----------- | ------------ |
| T-Mobile CGNAT            | d0ac5390 ↔ 8c21297d | IP + Quest 2 profile + unknown HMD            | AS21928 | 2,520       | Break        |
| IP-only (AT&T)            | fcaca6ff ↔ 6050f003 | Single IP only                                | AS7018  | 1           | Break        |
| Vodafone Germany          | e19ea28b ↔ 8ebb578c | 83 IPs + Quest 2 profile + unknown HMD        | AS3209  | 40,426      | Break        |
| Quest 3 profile (AT&T)    | e250efb9 ↔ 243c3ff0 | IP + Quest 3 profile + unknown HMD            | AS7018  | 9           | Break        |
| **Real alt (66-cluster)** | 165071a9 ↔ 001d2cb1 | IP + Quest 3 profile + **XPID** + unknown HMD | AS7018  | 1           | **Preserve** |

Known commodity profiles: `Meta Quest 2::WIFI::::Unknown::3::8::0::0`, `Meta Quest 3::WIFI::::Unknown::3::6::0::0`, `Meta Quest 3S::WIFI::::Unknown::3::6::0::0`

The Vodafone Germany case (AS3209) is not CGNAT per se but a dynamic IP pool that produces the same false-positive pattern. It needs either AS3209 in the CGNAT ASN list or heuristic detection.

## Test Summary

59 tests across 7 categories:

- **Detection (1-13):** ASN lookup (IPv4 + IPv6), CIDR matching (IPv4 + IPv6), binary search edge cases, garbage input
- **Heuristic (14-20):** Threshold, window, disabled, no-auto-exempt, memory cap, IPv6 tracking
- **Weak signal (21-32):** Combined detection, signal classification (IPv4/IPv6), settings hot-reload
- **Enforcement guard (33-38):** All-weak filtered, mixed kept, nil detector, transitional state
- **Cleanup (39-45):** Break, preserve, second-degree, idempotent, audit, unconditional write
- **Bug fix (46-50):** otherHistories populated, second-degree recompute, commodity filter
- **Integration (51-59):** End-to-end login, alt link creation (IPv4/IPv6), enforcement kick/reject

## Acceptance Criteria

1. Starlink IPs (IPv4 + IPv6) don't create false alt links
2. Enforcement guard provides immediate protection on deployment
3. Cleanup RPC breaks existing false links with audit trail
4. Suspensions don't cascade through CGNAT-only links
5. Real alts (shared HMD/XPID) still detected
6. Commodity Quest profiles treated as weak signals
7. Heuristic warns only, doesn't auto-act
8. Settings hot-reloadable
9. Graceful degradation on ASN failure
10. Non-blocking startup
11. Full IPv6 support across all detection layers
12. Commodity profiles filtered at write time
13. Second-degree alternates recompute correctly
14. All 59 tests pass
