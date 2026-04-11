# Per-Guild Active Features

## Goal

Allow guild admins to activate client-side feature plugins (e.g. April Fools mods) on a per-guild basis. The client receives the list at login and enables the corresponding mods. Different guilds can run different features independently.

## Architecture

Add `ActiveFeatures []string` to the existing `GroupMetadata` struct. At login, copy the active guild's `ActiveFeatures` into a new field on `GameSettings`, which is already sent to the client. No new storage, no new RPCs, no schedules.

## Changes

### 1. `GroupMetadata` (server/evr_group_metadata.go)

Add field:

```go
ActiveFeatures []string `json:"active_features,omitempty"`
```

This is managed via the existing guild metadata update flow (Discord bot commands, admin RPCs).

### 2. `GameSettings` (server/evr/login_settings.go)

Add field:

```go
ActiveFeatures []string `json:"active_features,omitempty"`
```

### 3. Login pipeline (server/evr_pipeline_login.go)

After constructing `gameSettings` and resolving the player's active guild, populate:

```go
if activeGuild != nil {
    gameSettings.ActiveFeatures = activeGuild.Metadata.ActiveFeatures
}
```

## Client contract

The client checks `GameSettings.ActiveFeatures` for known feature IDs (arbitrary strings like `"april_fools"`, `"low_gravity"`, `"big_head_mode"`). If a feature ID is present, the corresponding mod is enabled. If absent, the mod is disabled. Feature IDs are opaque to the server.

## Testing

- Unit test: `GameSettings` serialization round-trips with `ActiveFeatures` populated and empty.
- Integration test: login pipeline populates `ActiveFeatures` from the active guild's metadata.
