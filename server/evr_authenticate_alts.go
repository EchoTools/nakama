package server

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// disabledAccountIDs returns which of the given user IDs belong to accounts
// that are currently disabled.
//
// Returns an error rather than swallowing one, so callers choose their own
// failure posture. The two that gate on this choose differently and both are
// deliberate: the login rejection fails open (a lookup failure must not lock a
// legitimate player out, and the delayed kick still runs behind it), while a
// caller that already admitted the session can afford to be stricter.
func disabledAccountIDs(ctx context.Context, nk runtime.NakamaModule, userIDs []string) ([]string, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}

	accounts, err := nk.AccountsGetId(ctx, userIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to get accounts for user IDs %v: %w", userIDs, err)
	}

	disabled := make([]string, 0, len(accounts))
	for _, a := range accounts {
		// A zero DisableTime is "not disabled", not "disabled at the epoch" --
		// both the nil and the zero form appear in practice.
		if a.GetDisableTime() == nil || a.GetDisableTime().AsTime().IsZero() {
			continue
		}
		if a.GetUser() != nil {
			disabled = append(disabled, a.GetUser().GetId())
		}
	}
	if len(disabled) == 0 {
		return nil, nil
	}
	return disabled, nil
}

type AlternateSearchMatch struct {
	OtherUserID string   `json:"other_user_id"`
	Items       []string `json:"items"`
}

// AltSearchPatterns returns the keys used to DISCOVER candidate alternate
// accounts in the login-cache index.
//
// These must stay in step with the keys rebuildCache writes into the indexed
// `cache` field (LoginHistoryEntry.Items) and with the keys loginHistoryCompare
// forms edges on. A key that is written and compared but never searched is
// inert: the comparison only ever runs against candidates this query already
// returned, so an account whose ONLY overlap with a banned account is that key
// is never surfaced, never compared, and produces zero edges.
//
// SystemProfile is deliberately NOT among them. It stays a COMPARISON key --
// loginHistoryCompare still forms edges on it, and rebuildCache still writes it
// into the indexed cache -- so it can corroborate a candidate that an IP, HMD
// serial or XPID already surfaced. It may not surface one on its own.
//
// v3.27.2-evr.321 (56e9a9c2d) promoted it to a discovery key for #516, on the
// reading that a cheater who rotates IP, serial and XPID keeps the same
// machine. The premise does not hold: nothing in the string is machine-unique.
// It is headset_model::network_type::video_card::cpu_model plus four integers,
// so two people who bought the same headset produce identical bytes. Measured
// against production on 2026-08-12:
//
//	12,803 accounts share "Meta Quest 2::WIFI::::Unknown::3::8::0::0"
//	26,787 of 45,967 accounts sit in some collision group
//	 1,920 remain enforcement-eligible after the commodity-profile filter
//	    49 currently share a profile with a disabled account
//
// As a discovery key it returns strangers, and the comparison that follows then
// confirms the very key that surfaced them, so the collision is invisible in
// the resulting edge. Enforcement acting on those edges bans bystanders.
//
// This restores the pre-.321 behaviour and nothing more. Whether alt detection
// should have a real hardware fingerprint at all is a product question that
// this does not answer.
func (h *LoginHistory) AltSearchPatterns() []string {
	items := make([]string, 0, len(h.History)*3+len(h.XPIs))
	for _, e := range h.History {
		for _, s := range [...]string{
			e.ClientIP,
			e.LoginData.HMDSerialNumber,
			e.XPID.Token(),
		} {
			items = append(items, s)
		}
	}
	for xpi := range h.XPIs {
		items = append(items, xpi)
	}

	slices.Sort(items)
	items = slices.Compact(items)

	for i := 0; i < len(items); i++ {
		if matchIgnoredAltPattern(items[i]) {
			items = slices.Delete(items, i, i+1)
			i--
		}
	}

	if len(items) == 0 {
		return nil
	}
	return items
}

func LoginAlternateSearch(ctx context.Context, nk runtime.NakamaModule, loginHistory *LoginHistory, skipSelf bool) ([]*AlternateSearchMatch, map[string]*LoginHistory, error) {
	items := loginHistory.AltSearchPatterns()
	if len(items) == 0 {
		return nil, nil, nil
	}
	return LoginAlternatePatternSearch(ctx, nk, loginHistory, items, skipSelf)
}

// LoginAlternatePatternSearch searches for other users that have logged in with the same patterns as the given login history.
func LoginAlternatePatternSearch(ctx context.Context, nk runtime.NakamaModule, loginHistory *LoginHistory, items []string, skipSelf bool) ([]*AlternateSearchMatch, map[string]*LoginHistory, error) {

	query := fmt.Sprintf("+value.cache:%s", Query.CreateMatchPattern(items))
	otherHistories := make(map[string]*LoginHistory)
	matches := make([]*AlternateSearchMatch, 0)
	var err error
	var result *api.StorageObjects
	var cursor string

	seen := make(map[string]struct{}, 0)

	for {
		result, cursor, err = nk.StorageIndexList(ctx, SystemUserID, LoginHistoryCacheIndex, query, 100, nil, cursor)
		if err != nil {
			return nil, nil, fmt.Errorf("error listing alt index: %w", err)
		}

		for _, obj := range result.Objects {

			// Skip the current user.
			if skipSelf && obj.UserId == loginHistory.userID {
				continue
			}

			if _, found := seen[obj.UserId]; found {
				continue
			}
			seen[obj.UserId] = struct{}{}

			otherHistory := NewLoginHistory(obj.UserId)
			if err := StorableRead(ctx, nk, obj.UserId, otherHistory, false); err != nil {
				return nil, nil, fmt.Errorf("error reading alt history: %w", err)
			}
			otherHistories[obj.UserId] = otherHistory
			// Compare the entries.
			matches = append(matches, loginHistoryCompare(loginHistory, otherHistory)...)
		}

		if cursor == "" {
			break
		}
	}

	return matches, otherHistories, nil
}

func LoginDeniedClientIPAddressSearch(ctx context.Context, nk runtime.NakamaModule, clientIPAddress string) ([]string, error) {

	// regexEscapeForBluge, not Query.QuoteStringValue. The value is
	// interpolated into a REGEX clause, and QuoteStringValue escapes with a
	// single backslash -- which Bluge's query lexer strips from every
	// character in its reserved set, delivering the metacharacter bare to the
	// regex engine. See TestRegexEscapeForBluge_QuoteStringValueBroken.
	//
	// This runs on every login with a client-influenced address, so a value of
	// "[^x]*" was a match-everything regex over the whole login-cache index.
	query := fmt.Sprintf("+value.denied_client_addrs:/%s/", regexEscapeForBluge(clientIPAddress))
	// Perform the storage list operation

	// `var cursor` with plain assignment below, NOT `result, cursor, err :=`.
	// The short form declares a second cursor scoped to the loop body, leaving
	// the outer one -- the one handed back to StorageIndexList -- empty
	// forever: page one is re-fetched and re-appended without end. This is
	// called on every login, so the loop only had to be armed by a query
	// matching more than one page. Same shape as LoginAlternatePatternSearch
	// above, which is the correct one.
	var (
		cursor string
		result *api.StorageObjects
		err    error
	)
	userIDs := make([]string, 0)
	for {
		result, cursor, err = nk.StorageIndexList(ctx, SystemUserID, LoginHistoryCacheIndex, query, 10, nil, cursor)
		if err != nil {
			return nil, fmt.Errorf("error listing display name history: %w", err)
		}

		for _, obj := range result.Objects {
			userIDs = append(userIDs, obj.UserId)
		}

		if cursor == "" {
			break
		}
	}
	return userIDs, nil

}

// isMachineFingerprint reports whether an alt-match item is a specific machine
// fingerprint, as opposed to any of the other things a match can be keyed on.
//
// The distinction is the whole basis for treating this signal differently from
// the rest. An IP is a household and is rotated by a reboot; an HMD serial is
// rotated by editing a config. The full system profile is the one key that
// tracks the hardware someone actually owns.
//
// "Specific" is doing real work here. The same string is a bucket rather than a
// key in two cases, and both are excluded:
//
//   - matchIgnoredAltPattern drops the degenerate profile -- every account that
//     logs in without SystemInfo emits the identical Unknown::::::::0::0::0::0,
//     which links strangers to each other en masse.
//   - IsWeakSignal drops commodity headset prefixes: "Meta Quest 3::..." plus
//     stock numbers describes a large share of the player base.
//
// A nil detector only skips the commodity check. It does not turn the whole
// test into "true" -- an unavailable classifier must narrow what this matches,
// never widen it.
func isMachineFingerprint(item string, detector *CGNATDetector) bool {
	if len(strings.Split(item, "::")) != systemProfileComponents {
		return false
	}
	if matchIgnoredAltPattern(item) {
		return false
	}
	if detector != nil && detector.IsWeakSignal(item) {
		return false
	}
	return true
}

// machineMatchedAlts returns the subset of altIDs linked to this account by a
// specific machine fingerprint.
//
// Narrower than filterStrongAlts on purpose: that one keeps an alt if ANY of
// its match items is a strong signal, so an alt linked only by a residential IP
// on a non-CGNAT range survives it. This one asks the single question #516's
// item 1 is about -- is this the same machine?
func machineMatchedAlts(history *LoginHistory, altIDs []string, detector *CGNATDetector) []string {
	if history == nil || len(altIDs) == 0 {
		return nil
	}

	matched := make([]string, 0, len(altIDs))
	for _, altID := range altIDs {
		for _, m := range history.AlternateMatches[altID] {
			if slices.ContainsFunc(m.Items, func(item string) bool {
				return isMachineFingerprint(item, detector)
			}) {
				matched = append(matched, altID)
				break
			}
		}
	}
	if len(matched) == 0 {
		return nil
	}
	return matched
}

func loginHistoryCompare(a, b *LoginHistory) []*AlternateSearchMatch {
	if a == nil || b == nil || len(a.History) == 0 || len(b.History) == 0 {
		return nil // No history to compare.
	}
	if a.userID == b.userID {
		return nil // Skip self-comparison.
	}
	// Every match this function can emit carries the SAME OtherUserID (b.userID),
	// so the only information in the result is the SET of distinct values the two
	// accounts have in common. Emitting one match per matching entry-PAIR produced
	// len(a.History) * len(b.History) objects, all keyed identically.
	//
	// That Cartesian product is a remotely-triggerable memory bomb. A client
	// controls its own History size -- entries are keyed on
	// xpid.Token()+":"+clientIP (see loginHistoryEntryKey), so rotating either
	// mints a fresh entry on every login, and nothing caps the entry COUNT (the
	// only guard is the 5MB marshal prune in MarshalJSON). Two accounts each
	// carrying ~1,700 entries that share one uncommon SystemProfile yield
	// ~2.9M objects for that pair alone; UpdateAlternates then stores every one
	// of them under a single map key. Measured in production 2026-08-12:
	// 5.3M live objects, ~728 B each, ~3.9 GB, killed at the 6 GiB cgroup limit
	// eight times.
	//
	// Comparison stays POSITIONAL -- XPID only matches XPID, IP only matches IP,
	// and so on -- exactly as the pairwise loop did. Only the deduplication is new.
	// Memory is now O(distinct items) instead of O(N*M).
	const (
		fieldXPID = iota
		fieldClientIP
		fieldSystemProfile
		fieldHMDSerial
		fieldCount
	)
	entryItems := func(e *LoginHistoryEntry) [fieldCount]string {
		return [fieldCount]string{
			e.XPID.String(),
			e.ClientIP,
			e.SystemProfile(),
			e.LoginData.HMDSerialNumber,
		}
	}

	var seenA [fieldCount]map[string]struct{}
	for i := range seenA {
		seenA[i] = make(map[string]struct{})
	}
	for _, e := range a.History {
		for i, item := range entryItems(e) {
			if item != "" {
				seenA[i][item] = struct{}{}
			}
		}
	}

	matchingSet := make(map[string]struct{})
	for _, e := range b.History {
		for i, item := range entryItems(e) {
			if item == "" || matchIgnoredAltPattern(item) {
				continue
			}
			if _, ok := seenA[i][item]; ok {
				matchingSet[item] = struct{}{}
			}
		}
	}

	if len(matchingSet) == 0 {
		return nil
	}

	matchingItems := make([]string, 0, len(matchingSet))
	for item := range matchingSet {
		matchingItems = append(matchingItems, item)
	}
	slices.Sort(matchingItems) // deterministic output

	return []*AlternateSearchMatch{{
		OtherUserID: b.userID,
		Items:       matchingItems,
	}}
}
