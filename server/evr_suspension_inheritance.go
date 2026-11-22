package server

import (
	"fmt"
	"slices"
	"time"
)

// Values of SuspensionInheritanceFilter.Scope.
const (
	// InheritScopeAll inherits every enforcement. It is the default, and the empty
	// string means the same thing.
	InheritScopeAll = "all"
	// InheritScopeFullBans inherits only enforcements that also restrict private
	// matches, i.e. records issued with allow_private_lobbies=false.
	InheritScopeFullBans = "full_bans"
)

// SuspensionInheritanceFilter narrows what one inheritance link carries.
//
// A link is (inheriting guild, source guild). The inheriting guild owns it, so
// the filter is stored in the inheriting guild's GroupMetadata, keyed by source
// group ID, next to SuspensionInheritanceGroupIDs. The zero value inherits
// everything, which is what every link did before the filter existed: a link with
// no stored filter is unchanged.
type SuspensionInheritanceFilter struct {
	// MinimumDurationSeconds inherits only enforcements LONGER than this (strictly),
	// measured from the record's CreatedAt to its Expiry. Zero means no minimum.
	// A permanent enforcement (LifetimeDurationThreshold or longer) is longer than
	// any minimum.
	MinimumDurationSeconds int64 `json:"minimum_duration_seconds,omitempty"`
	// Scope is InheritScopeAll or InheritScopeFullBans. Empty means InheritScopeAll.
	Scope string `json:"scope,omitempty"`
}

// maxInheritanceMinimumDurationSeconds bounds MinimumDurationSeconds so that
// converting it to a time.Duration cannot overflow. Anything at or above the
// permanent threshold would exclude nothing but permanent records anyway, and
// those always pass.
const maxInheritanceMinimumDurationSeconds = int64(LifetimeDurationThreshold / time.Second)

// IsDefault reports whether the filter inherits everything.
func (f SuspensionInheritanceFilter) IsDefault() bool {
	return f.MinimumDurationSeconds <= 0 && (f.Scope == "" || f.Scope == InheritScopeAll)
}

// Validate rejects a filter that could not have been meant. It runs when a guild
// saves its metadata; Accepts stays total for filters that predate it.
func (f SuspensionInheritanceFilter) Validate() error {
	if f.MinimumDurationSeconds < 0 || f.MinimumDurationSeconds > maxInheritanceMinimumDurationSeconds {
		return fmt.Errorf("minimum_duration_seconds must be between 0 and %d, got %d", maxInheritanceMinimumDurationSeconds, f.MinimumDurationSeconds)
	}
	switch f.Scope {
	case "", InheritScopeAll, InheritScopeFullBans:
		return nil
	}
	return fmt.Errorf("scope must be %q or %q, got %q", InheritScopeAll, InheritScopeFullBans, f.Scope)
}

// Accepts reports whether the inheriting guild takes this record.
//
// An unrecognised scope is treated as InheritScopeAll: Validate stops one being
// saved, and if one is ever read the failure that keeps a ban in force is
// preferable to the one that drops it.
func (f SuspensionInheritanceFilter) Accepts(r GuildEnforcementRecord) bool {
	if f.Scope == InheritScopeFullBans && r.SuspensionExcludesPrivateLobbies() {
		return false
	}
	if f.MinimumDurationSeconds > 0 {
		d := r.Expiry.Sub(r.CreatedAt)
		if d >= LifetimeDurationThreshold {
			return true
		}
		return d > time.Duration(f.MinimumDurationSeconds)*time.Second
	}
	return true
}

// InheritanceFilterFor returns the filter this guild applies to enforcements
// inherited from srcGroupID. A link with no stored filter yields the zero value.
func (g *GroupMetadata) InheritanceFilterFor(srcGroupID string) SuspensionInheritanceFilter {
	if g == nil {
		return SuspensionInheritanceFilter{}
	}
	return g.SuspensionInheritanceFilters[srcGroupID]
}

// ValidateSuspensionInheritanceFilters checks every stored filter and that each
// one belongs to a link the guild actually has: a filter for a source that is not
// in SuspensionInheritanceGroupIDs would silently do nothing, which reads as
// protection that is not there.
func (g *GroupMetadata) ValidateSuspensionInheritanceFilters() error {
	if g == nil {
		return nil
	}
	for srcGroupID, f := range g.SuspensionInheritanceFilters {
		if !slices.Contains(g.SuspensionInheritanceGroupIDs, srcGroupID) {
			return fmt.Errorf("suspension_inheritance_filters: %q is not in suspension_inheritence_group_ids", srcGroupID)
		}
		if err := f.Validate(); err != nil {
			return fmt.Errorf("suspension_inheritance_filters[%q]: %w", srcGroupID, err)
		}
	}
	return nil
}

// InheritanceFilters holds the non-default filters of every inheritance link:
// map[srcGroupID]map[inheritingGroupID]filter. A missing entry, and a nil map,
// mean the link inherits everything.
type InheritanceFilters map[string]map[string]SuspensionInheritanceFilter

// For returns the filter on the link from srcGroupID to inheritingGroupID.
func (f InheritanceFilters) For(srcGroupID, inheritingGroupID string) SuspensionInheritanceFilter {
	return f[srcGroupID][inheritingGroupID]
}
