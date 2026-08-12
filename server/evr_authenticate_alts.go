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
// SystemProfile was in exactly that state — captured in Items, indexed, and
// compared, but absent here. A cheater rotating IP, HMD serial and XPID (all
// trivially changed) kept the same machine and linked to nothing. That is #516.
func (h *LoginHistory) AltSearchPatterns() []string {
	items := make([]string, 0, len(h.History)*4+len(h.XPIs))
	for _, e := range h.History {
		for _, s := range [...]string{
			e.ClientIP,
			e.LoginData.HMDSerialNumber,
			e.XPID.Token(),
			// The machine fingerprint. Rotating an account does not rotate the
			// hardware. Commodity profiles (Quest headsets, and any profile with
			// no hardware identity at all) are dropped by matchIgnoredAltPattern
			// below, so this adds a discovery key only where it is a fingerprint
			// rather than a bucket.
			e.SystemProfile(),
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
	matches := make([]*AlternateSearchMatch, 0)

	// Collect the authUserData from both histories.
	authUserData := make([][][]string, 2)
	for i, h := range []map[string]*LoginHistoryEntry{a.History, b.History} {
		authUserData[i] = make([][]string, 0, len(h))
		for _, e := range h {
			items := []string{
				e.XPID.String(),
				e.ClientIP,
				e.SystemProfile(),
				e.LoginData.HMDSerialNumber,
			}
			authUserData[i] = append(authUserData[i], items)
		}
	}
	// Compare the entries from both histories.
	for _, itemsA := range authUserData[0] {
		for _, itemsB := range authUserData[1] {
			matchingItems := make([]string, 0, len(itemsA))
			for i, item := range itemsA {
				if item == itemsB[i] && item != "" && !matchIgnoredAltPattern(item) {
					// The items match.
					matchingItems = append(matchingItems, item)
				}
			}
			// If there are matching items, create a match entry.
			if len(matchingItems) > 0 {
				matches = append(matches, &AlternateSearchMatch{
					OtherUserID: b.userID,
					Items:       matchingItems,
				})
			}
		}
	}
	return matches
}
