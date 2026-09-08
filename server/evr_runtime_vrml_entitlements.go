package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type VRMLSeasonID string
type VRMLPrestige int

const (
	VRMLPlayer VRMLPrestige = iota
	VRMLFinalist
	VRMLChampion
)

const (
	MedalVRMLPreseason  = "rwd_medal_s1_vrml_preseason"
	MedalVRMLS1         = "rwd_medal_s1_vrml_s1_user"
	MedalVRMLS1Champion = "rwd_medal_s1_vrml_s1_champion"
	MedalVRMLS1Finalist = "rwd_medal_s1_vrml_s1_finalist"
	MedalVRMLS2         = "rwd_medal_s1_vrml_s2"
	MedalVRMLS2Champion = "rwd_medal_s1_vrml_s2_champion"
	MedalVRMLS2Finalist = "rwd_medal_s1_vrml_s2_finalist"
	MedalVRMLS3         = "rwd_medal_s1_vrml_s3"
	MedalVRMLS3Champion = "rwd_medal_s1_vrml_s3_champion"
	MedalVRMLS3Finalist = "rwd_medal_s1_vrml_s3_finalist"
	MedalVRMLS4         = "rwd_medal_0006"
	MedalVRMLS4Finalist = "rwd_medal_0007"
	MedalVRMLS4Champion = "rwd_medal_0008"

	TagVRMLPreseason  = "rwd_tag_s1_vrml_preseason"
	TagVRMLS1         = "rwd_tag_s1_vrml_s1"
	TagVRMLS1Champion = "rwd_tag_s1_vrml_s1_champion"
	TagVRMLS1Finalist = "rwd_tag_s1_vrml_s1_finalist"
	TagVRMLS2         = "rwd_tag_s1_vrml_s2"
	TagVRMLS2Champion = "rwd_tag_s1_vrml_s2_champion"
	TagVRMLS2Finalist = "rwd_tag_s1_vrml_s2_finalist"
	TagVRMLS3         = "rwd_tag_s1_vrml_s3"
	TagVRMLS3Champion = "rwd_tag_s1_vrml_s3_champion"
	TagVRMLS3Finalist = "rwd_tag_s1_vrml_s3_finalist"
	TagVRMLS4         = "rwd_tag_0008"
	TagVRMLS4Champion = "rwd_tag_0010"
	TagVRMLS4Finalist = "rwd_tag_0009"
	TagVRMLS5         = "rwd_tag_0035"
	TagVRMLS5Champion = "rwd_tag_0037"
	TagVRMLS5Finalist = "rwd_tag_0036"
	// rwd_tag_0038: Stand store item; rwd_tag_0039: Liminal event item.
	TagStand          = "rwd_tag_0038"
	TagLiminal        = "rwd_tag_0039"
	TagVRMLS6         = "rwd_tag_0040"
	TagVRMLS6Champion = "rwd_tag_0042"
	TagVRMLS6Finalist = "rwd_tag_0041"
	TagVRMLS7         = "rwd_tag_0043"
	TagVRMLS7Champion = "rwd_tag_0045"
	TagVRMLS7Finalist = "rwd_tag_0044"
)

const (
	VRMLPreSeason VRMLSeasonID = "b-VHi9XjGIR0Pv17C7P_Tw2"
	VRMLSeason1   VRMLSeasonID = "PS5eV-VOdnRPCAxRix9xlQ2"
	VRMLSeason2   VRMLSeasonID = "NLQNdfR3j0lyy6eLpLhIYw2"
	VRMLSeason3   VRMLSeasonID = "XPtJ0s7XBpsbDHrjS0e_3g2"
	VRMLSeason4   VRMLSeasonID = "YJoYnb3iWN8EcCrs92U04A2"
	VRMLSeason5   VRMLSeasonID = "XKVgWTi5AdpkSQnLhhs7bw2"
	VRMLSeason6   VRMLSeasonID = "n0PsCrBx_-rpjiXABiftSw2"
	VRMLSeason7   VRMLSeasonID = "la1xDIFPC6E-0eo429SKFA2"

	VRMLEchoArenaShortName = "EchoArena"
)

var vrmlSeasonDescriptionMap = map[VRMLSeasonID]string{
	VRMLPreSeason: "Pre-Season",
	VRMLSeason1:   "Season 1",
	VRMLSeason2:   "Season 2",
	VRMLSeason3:   "Season 3",
	VRMLSeason4:   "Season 4",
	VRMLSeason5:   "Season 5",
	VRMLSeason6:   "Season 6",
	VRMLSeason7:   "Season 7",
}

// formatVRMLSeasonName converts a season ID to a short, user-friendly display name.
// Examples:
//   - VRMLSeason5 ("Season 5") → "S5"
//   - VRMLSeason7 ("Season 7") → "S7"
//   - VRMLPreSeason ("Pre-Season") → "Pre-Season"
//   - Unknown season ID → returns the raw season ID string
func formatVRMLSeasonName(seasonID VRMLSeasonID) string {
	seasonName, ok := vrmlSeasonDescriptionMap[seasonID]
	if !ok {
		return string(seasonID)
	}
	// Convert "Season N" to "SN" format
	if sNumber, found := strings.CutPrefix(seasonName, "Season "); found {
		return "S" + sNumber
	}
	return seasonName
}

var vrmlCosmeticMap = map[VRMLSeasonID]map[VRMLPrestige][]string{
	VRMLPreSeason: {
		VRMLPlayer: {TagVRMLPreseason, MedalVRMLPreseason},
	},
	VRMLSeason1: {
		VRMLPlayer:   {TagVRMLS1, MedalVRMLS1},
		VRMLFinalist: {MedalVRMLS1Finalist, TagVRMLS1Finalist, TagVRMLS1, MedalVRMLS1},
		VRMLChampion: {MedalVRMLS1Champion, TagVRMLS1Champion, MedalVRMLS1Finalist, TagVRMLS1Finalist, TagVRMLS1, MedalVRMLS1},
	},
	VRMLSeason2: {
		VRMLPlayer:   {TagVRMLS2, MedalVRMLS2},
		VRMLFinalist: {MedalVRMLS2Finalist, TagVRMLS2Finalist, TagVRMLS2, MedalVRMLS2},
		VRMLChampion: {MedalVRMLS2Champion, TagVRMLS2Champion, MedalVRMLS2Finalist, TagVRMLS2Finalist, TagVRMLS2, MedalVRMLS2},
	},
	VRMLSeason3: {
		VRMLPlayer:   {TagVRMLS3, MedalVRMLS3},
		VRMLFinalist: {MedalVRMLS3Finalist, TagVRMLS3Finalist, TagVRMLS3, MedalVRMLS3},
		VRMLChampion: {TagVRMLS3Champion, MedalVRMLS3Champion, MedalVRMLS3Finalist, TagVRMLS3Finalist, TagVRMLS3, MedalVRMLS3},
	},
	VRMLSeason4: {
		VRMLPlayer:   {TagVRMLS4, MedalVRMLS4},
		VRMLFinalist: {TagVRMLS4Finalist, MedalVRMLS4Finalist, TagVRMLS4, MedalVRMLS4},
		VRMLChampion: {TagVRMLS4Champion, MedalVRMLS4Champion, TagVRMLS4Finalist, MedalVRMLS4Finalist, TagVRMLS4, MedalVRMLS4},
	},
	VRMLSeason5: {
		VRMLPlayer:   {TagVRMLS5},
		VRMLFinalist: {TagVRMLS5Finalist, TagVRMLS5},
		VRMLChampion: {TagVRMLS5Champion, TagVRMLS5Finalist, TagVRMLS5},
	},
	VRMLSeason6: {
		VRMLPlayer:   {TagVRMLS6},
		VRMLFinalist: {TagVRMLS6Finalist, TagVRMLS6},
		VRMLChampion: {TagVRMLS6Champion, TagVRMLS6Finalist, TagVRMLS6},
	},
	VRMLSeason7: {
		VRMLPlayer:   {TagVRMLS7},
		VRMLFinalist: {TagVRMLS7Finalist, TagVRMLS7},
		VRMLChampion: {TagVRMLS7Champion, TagVRMLS7Finalist, TagVRMLS7},
	},
}

type VRMLEntitlement struct {
	SeasonID VRMLSeasonID `json:"season_id"`
	Prestige VRMLPrestige `json:"prestige"`
}

func (e VRMLEntitlement) MarshalText() ([]byte, error) {
	switch e.Prestige {
	case VRMLPlayer:
		return []byte(e.SeasonID + ":player"), nil
	case VRMLFinalist:
		return []byte(e.SeasonID + ":finalist"), nil
	case VRMLChampion:
		return []byte(e.SeasonID + ":champion"), nil
	default:
		return nil, fmt.Errorf("invalid VRML prestige: %d", e.Prestige)
	}
}

func (e *VRMLEntitlement) UnmarshalText(text []byte) error {

	seasonID, prestige, found := strings.Cut(string(text), ":")
	if !found {
		return fmt.Errorf("invalid VRML entitlement: %s", text)
	}
	e.SeasonID = VRMLSeasonID(seasonID)
	switch prestige {
	case "player":
		e.Prestige = VRMLPlayer
	case "finalist":
		e.Prestige = VRMLFinalist
	case "champion":
		e.Prestige = VRMLChampion
	default:
		return fmt.Errorf("invalid VRML prestige: %s", prestige)
	}
	return nil
}

func (e VRMLEntitlement) Cosmetics() []string {
	return append(vrmlCosmeticMap[e.SeasonID][e.Prestige], []string{"decal_vrml_a", "emote_vrml_a"}...)
}

// AllVRMLCosmetics returns the wallet key for every possible VRML cosmetic across all seasons and prestige levels.
// Used to compute the revocation set when re-assigning entitlements on link.
func AllVRMLCosmetics() []string {
	seen := make(map[string]struct{})
	var out []string
	for _, prestigeMap := range vrmlCosmeticMap {
		for _, ids := range prestigeMap {
			for _, id := range ids {
				key := "cosmetic:arena:" + id
				if _, ok := seen[key]; !ok {
					seen[key] = struct{}{}
					out = append(out, key)
				}
			}
		}
	}
	// universal VRML cosmetics
	for _, id := range []string{"decal_vrml_a", "emote_vrml_a"} {
		key := "cosmetic:arena:" + id
		if _, ok := seen[key]; !ok {
			out = append(out, key)
		}
	}
	return out
}

// vrmlEntitledWalletKeys is the set of wallet keys the given entitlement set
// legitimately covers. VRMLRevocationChangeset excludes exactly this set;
// VRMLAssignmentChangeset writes exactly this set. Both derive it from here so
// the two can never drift apart — see VRMLEntitlementWalletUpdates for why that
// disjointness is load-bearing.
func vrmlEntitledWalletKeys(entitlements []*VRMLEntitlement) map[string]struct{} {
	entitled := make(map[string]struct{})
	for _, e := range entitlements {
		for _, id := range e.Cosmetics() {
			entitled["cosmetic:arena:"+id] = struct{}{}
		}
	}
	return entitled
}

// VRMLRevocationChangeset returns the wallet deltas that zero every known VRML
// cosmetic the entitlement set does not cover. It is pure: the caller supplies
// the wallet, so the changeset can be computed from a read the caller already
// holds and committed in a transaction alongside other operations.
//
// An empty result means there is nothing to revoke, and the caller should emit
// no wallet update at all rather than an empty one — an empty changeset still
// writes a wallet_ledger row.
func VRMLRevocationChangeset(wallet map[string]int64, entitlements []*VRMLEntitlement) map[string]int64 {
	entitled := vrmlEntitledWalletKeys(entitlements)

	changeset := make(map[string]int64)
	for _, key := range AllVRMLCosmetics() {
		if _, ok := entitled[key]; ok {
			continue // legitimately entitled, leave it
		}
		if v := wallet[key]; v != 0 {
			changeset[key] = -v // zero it out
		}
	}
	return changeset
}

// VRMLAssignmentChangeset returns the wallet deltas that bring every cosmetic
// covered by the entitlement set to exactly 1. Pure, for the same reason as
// VRMLRevocationChangeset.
func VRMLAssignmentChangeset(wallet map[string]int64, entitlements []*VRMLEntitlement) map[string]int64 {
	changeset := make(map[string]int64, len(entitlements))
	for _, e := range entitlements {
		for _, cosmeticID := range e.Cosmetics() {
			name := "cosmetic:arena:" + cosmeticID

			// Make sure it is 1
			if v, ok := wallet[name]; !ok || v != 1 {
				changeset[name] = v*-1 + 1
			}
		}
	}
	return changeset
}

// vrmlRevocationMetadata and vrmlAssignmentMetadata are kept separate so that
// batching the two wallet updates into one transaction still writes two
// distinguishable wallet_ledger rows. updateWallets (core_wallet.go) appends one
// ledger row per *walletUpdate, applying them in order against the same
// in-transaction wallet map, so two entries for one user compose correctly.
func vrmlRevocationMetadata(assignerID, assignerUsername, vrmlUserID string) map[string]any {
	return map[string]any{
		"assigner_username": assignerUsername,
		"assigner_id":       assignerID,
		"vrml_user_id":      vrmlUserID,
		"action":            "revoke_non_entitled",
	}
}

func vrmlAssignmentMetadata(assignerID, assignerUsername, vrmlUserID string, entitlements []*VRMLEntitlement) map[string]any {
	return map[string]any{
		"assigner_username": assignerUsername,
		"assigner_id":       assignerID,
		"vrml_user_id":      vrmlUserID,
		"entitlements":      entitlements,
	}
}

// vrmlLoadWallet reads and decodes a user's wallet.
func vrmlLoadWallet(ctx context.Context, nk runtime.NakamaModule, userID string) (map[string]int64, error) {
	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get account for %s: %w", userID, err)
	}

	wallet := make(map[string]int64)
	if err := json.Unmarshal([]byte(account.Wallet), &wallet); err != nil {
		return nil, status.Error(codes.Internal, "failed to unmarshal wallet")
	}
	return wallet, nil
}

// VRMLEntitlementWalletUpdates reads the wallet ONCE and returns the revocation
// and the assignment as runtime.WalletUpdate operations, for a caller that wants
// to commit them in a single MultiUpdate transaction alongside its storage
// writes. The revocation is first; it is omitted entirely when there is nothing
// to revoke.
//
// Why one read is sound. The sequential form (RevokeNonEntitledVRMLCosmetics
// then AssignEntitlements) re-read the wallet in between, so the question is
// whether the revocation write can change any value the assignment reads. It
// cannot: VRMLRevocationChangeset only ever emits keys it has excluded via
// vrmlEntitledWalletKeys, and VRMLAssignmentChangeset only ever emits keys that
// set contains. The two key sets are disjoint by construction, so the assignment
// deltas computed from the pre-revocation wallet equal the ones computed from
// the post-revocation wallet. TestVRMLEntitlementChangesets_Disjoint and
// TestVRMLEntitlementChangesets_MatchSequentialApplication pin both halves.
func VRMLEntitlementWalletUpdates(ctx context.Context, nk runtime.NakamaModule, assignerID, assignerUsername, userID, vrmlUserID string, entitlements []*VRMLEntitlement) ([]*runtime.WalletUpdate, error) {
	wallet, err := vrmlLoadWallet(ctx, nk, userID)
	if err != nil {
		return nil, err
	}

	updates := make([]*runtime.WalletUpdate, 0, 2)
	if revocation := VRMLRevocationChangeset(wallet, entitlements); len(revocation) > 0 {
		updates = append(updates, &runtime.WalletUpdate{
			UserID:    userID,
			Changeset: revocation,
			Metadata:  vrmlRevocationMetadata(assignerID, assignerUsername, vrmlUserID),
		})
	}
	updates = append(updates, &runtime.WalletUpdate{
		UserID:    userID,
		Changeset: VRMLAssignmentChangeset(wallet, entitlements),
		Metadata:  vrmlAssignmentMetadata(assignerID, assignerUsername, vrmlUserID, entitlements),
	})
	return updates, nil
}

// RevokeNonEntitledVRMLCosmetics zeros out any VRML cosmetic wallet entries that are not covered by
// the provided entitlement set. Call this before AssignEntitlements when re-linking an account so that
// previously granted cosmetics from a different (or fraudulent) VRML account are cleared.
//
// This is the standalone form, for callers that revoke without also assigning
// (the unlink path). A caller doing both should use
// VRMLEntitlementWalletUpdates, which needs one wallet read instead of two and
// commits both in one transaction.
func RevokeNonEntitledVRMLCosmetics(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, assignerID, assignerUsername, userID, vrmlUserID string, entitlements []*VRMLEntitlement) error {
	wallet, err := vrmlLoadWallet(ctx, nk, userID)
	if err != nil {
		return err
	}

	changeset := VRMLRevocationChangeset(wallet, entitlements)
	if len(changeset) == 0 {
		return nil // nothing to revoke
	}

	metadata := vrmlRevocationMetadata(assignerID, assignerUsername, vrmlUserID)

	if _, _, err := nk.WalletUpdate(ctx, userID, changeset, metadata, true); err != nil {
		return fmt.Errorf("failed to revoke cosmetics for %s: %w", userID, err)
	}

	logger.WithFields(map[string]any{
		"assigner_id":      assignerID,
		"user_id":          userID,
		"vrml_user_id":     vrmlUserID,
		"cosmetic_changes": changeset,
	}).Info("revoked non-entitled VRML cosmetics")

	return nil
}

// AssignEntitlements brings every cosmetic the entitlement set covers to 1.
//
// This is the standalone form, for callers that assign without also revoking
// (the Discord admin grant). A caller doing both should use
// VRMLEntitlementWalletUpdates.
func AssignEntitlements(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, assignerID, assignerUsername, userID, vrmlUserID string, entitlements []*VRMLEntitlement) error {

	wallet, err := vrmlLoadWallet(ctx, nk, userID)
	if err != nil {
		return err
	}

	changeset := VRMLAssignmentChangeset(wallet, entitlements)

	metadata := vrmlAssignmentMetadata(assignerID, assignerUsername, vrmlUserID, entitlements)

	if _, _, err := nk.WalletUpdate(ctx, userID, changeset, metadata, true); err != nil {
		return fmt.Errorf("failed to update wallet for %s: %w", userID, err)
	}

	// Log the action
	logger.WithFields(map[string]any{
		"assigner_id":      assignerID,
		"user_id":          userID,
		"vrml_user_id":     vrmlUserID,
		"entitlements":     entitlements,
		"cosmetic_changes": changeset,
	}).Info("assigned VRML entitlements")

	return nil
}
