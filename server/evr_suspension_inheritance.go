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
	// ExcludeRules: a record matching any of these is not inherited. Checked first.
	ExcludeRules []SuspensionInheritanceRule `json:"exclude_rules,omitempty"`
	// IncludeRules: otherwise, a record matching any of these is inherited.
	IncludeRules []SuspensionInheritanceRule `json:"include_rules,omitempty"`
	// Default decides a record that matches no rule: InheritDefaultInclude or
	// InheritDefaultExclude. Empty means InheritDefaultInclude, which is what every
	// link did before rules existed.
	Default string `json:"default,omitempty"`
}

// SuspensionInheritanceRule matches a record whose string property Key equals
// Value exactly (case-sensitive, no wildcards). See GuildEnforcementRecord.StringProperty.
type SuspensionInheritanceRule struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Values of SuspensionInheritanceFilter.Default.
const (
	InheritDefaultInclude = "include"
	InheritDefaultExclude = "exclude"
)

// maxInheritanceMinimumDurationSeconds bounds MinimumDurationSeconds so that
// converting it to a time.Duration cannot overflow. Anything at or above the
// permanent threshold would exclude nothing but permanent records anyway, and
// those always pass.
const maxInheritanceMinimumDurationSeconds = int64(LifetimeDurationThreshold / time.Second)

// IsDefault reports whether the filter inherits everything.
func (f SuspensionInheritanceFilter) IsDefault() bool {
	return f.MinimumDurationSeconds <= 0 && (f.Scope == "" || f.Scope == InheritScopeAll) &&
		len(f.ExcludeRules) == 0 && len(f.IncludeRules) == 0 && f.Default != InheritDefaultExclude
}

// Validate rejects a filter that could not have been meant. It runs when a guild
// saves its metadata; Accepts stays total for filters that predate it.
func (f SuspensionInheritanceFilter) Validate() error {
	if f.MinimumDurationSeconds < 0 || f.MinimumDurationSeconds > maxInheritanceMinimumDurationSeconds {
		return fmt.Errorf("minimum_duration_seconds must be between 0 and %d, got %d", maxInheritanceMinimumDurationSeconds, f.MinimumDurationSeconds)
	}
	switch f.Scope {
	case "", InheritScopeAll, InheritScopeFullBans:
	default:
		return fmt.Errorf("scope must be %q or %q, got %q", InheritScopeAll, InheritScopeFullBans, f.Scope)
	}
	switch f.Default {
	case "", InheritDefaultInclude, InheritDefaultExclude:
	default:
		return fmt.Errorf("default must be %q or %q, got %q", InheritDefaultInclude, InheritDefaultExclude, f.Default)
	}
	if err := validateInheritanceRules("exclude_rules", f.ExcludeRules); err != nil {
		return err
	}
	if err := validateInheritanceRules("include_rules", f.IncludeRules); err != nil {
		return err
	}
	for _, r := range f.IncludeRules {
		if slices.Contains(f.ExcludeRules, r) {
			return fmt.Errorf("rule %q=%q is in both include_rules and exclude_rules", r.Key, r.Value)
		}
	}
	return nil
}

func validateInheritanceRules(name string, rules []SuspensionInheritanceRule) error {
	if len(rules) > maxInheritanceRulesList {
		return fmt.Errorf("%s: at most %d rules, got %d", name, maxInheritanceRulesList, len(rules))
	}
	for i, r := range rules {
		if r.Key == "" {
			return fmt.Errorf("%s[%d]: key must not be empty", name, i)
		}
		if len(r.Key) > maxStringPropertyBytes || len(r.Value) > maxStringPropertyBytes {
			return fmt.Errorf("%s[%d]: key and value must each be at most %d bytes", name, i, maxStringPropertyBytes)
		}
		if slices.Contains(rules[:i], r) {
			return fmt.Errorf("%s[%d]: duplicate rule %q=%q", name, i, r.Key, r.Value)
		}
	}
	return nil
}

// Accepts reports whether the inheriting guild takes this record. The gates run
// in order: scope, minimum duration, then the string-property rules (a match on
// any exclude rule rejects; otherwise a match on any include rule accepts;
// otherwise Default decides). A permanent record passes the minimum duration but
// still faces the rules.
//
// An unrecognised scope is treated as InheritScopeAll, and an unrecognised
// Default as InheritDefaultInclude: Validate stops one being saved, and if one is
// ever read the failure that keeps a ban in force is preferable to the one that
// drops it.
func (f SuspensionInheritanceFilter) Accepts(r GuildEnforcementRecord) bool {
	if f.Scope == InheritScopeFullBans && r.SuspensionExcludesPrivateLobbies() {
		return false
	}
	if f.MinimumDurationSeconds > 0 {
		d := r.Expiry.Sub(r.CreatedAt)
		if d < LifetimeDurationThreshold && d <= time.Duration(f.MinimumDurationSeconds)*time.Second {
			return false
		}
	}
	for _, rule := range f.ExcludeRules {
		if rule.Matches(r) {
			return false
		}
	}
	for _, rule := range f.IncludeRules {
		if rule.Matches(r) {
			return true
		}
	}
	return f.Default != InheritDefaultExclude
}

// Matches reports whether r carries Key with exactly Value. A record without the
// key never matches, not even a rule whose Value is empty.
func (rule SuspensionInheritanceRule) Matches(r GuildEnforcementRecord) bool {
	v, ok := r.StringProperty(rule.Key)
	return ok && v == rule.Value
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
