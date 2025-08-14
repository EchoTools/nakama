package server

var _ = IndexedVersionedStorable(&ProfileOptions{})

type ProfileOptions struct {
	DisableAFKTimeout      bool   `json:"disable_afk_timeout"`       // Disable AFK detection
	AllowBrokenCosmetics   bool   `json:"allow_broken_cosmetics"`    // Allow broken cosmetics
	EnableAllCosmetics     bool   `json:"enable_all_cosmetics"`      // Enable all cosmetics
	IsGlobalDeveloper      bool   `json:"is_global_developer"`       // Is a global developer
	IsGlobalOperator       bool   `json:"is_global_operator"`        // Is a global operator
	GoldDisplayNameActive  bool   `json:"gold_display_name"`         // The gold name display name
	RelayMessagesToDiscord bool   `json:"relay_messages_to_discord"` // Relay messages to Discord
	DisabledAccountMessage string `json:"disabled_account_message"`  // The disabled account message that the user will see.
	version                int    `json:"version"`                   // Version of the profile options
	userID                 string `json:"user_id"`                   // The user ID this profile options belongs to
}

// Ensure UserSettings implements IndexedVersionedStorable
var _ IndexedVersionedStorable = (*ProfileOptions)(nil)

func (u *ProfileOptions) StorageCollection() string {
	return "Profile"
}

func (u *ProfileOptions) StorageKey() string {
	// This should be set to the user's ID or a unique identifier.
	// Placeholder: return empty string, should be set by caller.
	return "options"
}

func (u *ProfileOptions) StorageVersion() int {
	return 1
}

func (u *ProfileOptions) StorageIndex() string {
	// This could be used for secondary indexing, e.g. by username or email.
	return ""
}
