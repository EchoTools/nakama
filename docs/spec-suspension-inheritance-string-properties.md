# Spec: filtering inherited suspensions by string properties

For portal developers. This describes what nakama stores on a suspension and how
a guild that inherits suspensions from another guild decides which ones it takes.
Code: `server/evr_enforcement_string_properties.go` and
`server/evr_suspension_inheritance.go`.

## 1. Every suspension record carries string properties

A suspension record (`GuildEnforcementRecord`) has `string_properties`, a map of
string to string. Nakama stores it and **never interprets what a property means**.
`tier: high` is just two strings to nakama.

Properties come from two places.

**The issuer.** The portal (or any caller of the RPCs below) sends whatever
properties it wants when it writes the record, for example `{"tier": "high"}`.

**Nakama.** Nakama also writes its own facts from the record's fields. These keys
are **nakama-owned**:

| key | value |
|---|---|
| `permanent` | `"true"` if the suspension lasts 3 years or more (`LifetimeDurationThreshold`), else `"false"` |
| `allow_privates` | `"true"` / `"false"`: the player may still join private lobbies |
| `community_values_required` | `"true"` / `"false"` |
| `is_publicly_visible` | `"true"` / `"false"` |
| `rule_violated` | the record's rule key, **only when it has one**. The key is absent otherwise |

Rules for nakama-owned keys:

- **Nakama's value always wins.** If a caller sends one of these keys, nakama
  overwrites it with its own value and logs a warning naming the key. Don't send
  them.
- They're recomputed on every write. Editing the expiry to 3+ years flips
  `permanent` to `"true"`, and editing `allow_private_lobbies` flips
  `allow_privates`.
- Every write path sets them, including Discord `/kick-player`, Discord guild-ban
  lifetime records and automatic suspensions. Those paths set no issuer
  properties, so their records carry only nakama-owned keys.
- **Records written before this existed** have no map. When a filter reads a
  nakama-owned key from such a record, nakama derives it from the record's
  fields. An old lifetime ban therefore matches `permanent = true`. Issuer keys
  like `tier` are never derived, so an old record has none.

## 2. Writing properties: the kick and edit RPCs

### `enforcement/kick`

```json
{ "group_id": "...", "target_user_id": "...", "user_notice": "...",
  "suspension_duration": "3d",
  "community_values_required": true,
  "string_properties": { "tier": "high" } }
```

- `string_properties` is stored on the new suspension record. A kick that adds no
  suspension (a void, for example) ignores it.
- Each key must be non-empty, each key and each value at most 64 bytes, and there
  are at most 32 keys. Values must be non-empty. Breaking any of these gives
  `InvalidArgument`, and nothing is written.
- `community_values_required` is accepted. It's the portal's name. The older
  `require_community_values` still works, and either one set to `true` requires
  community values.

### `enforcement/record/edit`

```json
{ "group_id": "...", "target_user_id": "...", "record_id": "...",
  "community_values_required": false,
  "string_properties": { "tier": "low", "source": "" } }
```

- `string_properties` is **merged** into the record's. A key you send is set, and
  a key sent with value `""` is deleted. Keys you don't send are kept. Leave the
  whole field out to change nothing.
- `community_values_required` sets the requirement. Leave it out to change
  nothing.
- An edit that changes only properties is a real edit. An edit that changes
  nothing at all is still refused with "No changes specified".
- Same limits as kick, except that `""` is allowed (it deletes).
- The 32-key limit also holds on the **record after the merge**, not just on the
  request. Only issuer keys count; the nakama-owned keys don't. An edit that
  would leave the record with more than 32 issuer keys is refused with
  `InvalidArgument` (HTTP 400) naming the limit, and nothing is written. Delete
  keys (send them with `""`) in the same edit to make room.
- The record's edit log records the properties before and after the edit.

### Unknown keys are logged

Both RPCs log, at warn, the **names** of any top-level JSON keys they don't
recognise (never the values). If the portal sends a field nakama doesn't read, it
shows up in the server log as `unknown_keys` instead of being dropped silently.

## 3. Filtering: what an inheriting guild takes

A guild that inherits from a source guild can set a filter on that link. It's
stored in the inheriting guild's metadata under
`suspension_inheritance_filters[<source group id>]`:

```json
{
  "minimum_duration_seconds": 604800,
  "scope": "full_bans",
  "exclude_rules": [ { "key": "tier", "value": "low" } ],
  "include_rules": [ { "key": "permanent", "value": "true" } ],
  "default": "exclude"
}
```

`minimum_duration_seconds` and `scope` work as before. The new fields:

- `exclude_rules`: a list of `{key, value}`.
- `include_rules`: a list of `{key, value}`.
- `default`: `"include"` or `"exclude"`. Empty or missing means `"include"`.

A rule matches a record when the record has that key with **exactly** that value.
Matching is case-sensitive, with no trimming and no wildcards. A record without
the key never matches, not even a rule whose value is `""`.

### Decision order for one record

1. `scope` is `full_bans` and the record allows private lobbies: **not inherited**.
2. `minimum_duration_seconds` is set and the record isn't longer than it: **not
   inherited**. A permanent record always passes this step, but **it still goes
   on to steps 3-5**.
3. The record matches **any** exclude rule: **not inherited**.
4. Otherwise, the record matches **any** include rule: **inherited**.
5. Otherwise `default` decides. An empty default means inherited.

So exclude beats include. A record with no properties matches no rule and falls
to the default.

### Existing guilds

A guild that inherits today and sets none of the new fields gets default
`include` with no rules, so it inherits exactly what it did before.

### Validation when a guild saves

A save is refused when any of these hold:

- `default` is anything other than `""`, `"include"` or `"exclude"`.
- A rule's key is empty.
- A rule's key or value is over 64 bytes.
- A list has more than 32 rules.
- The same rule appears twice in one list.
- The same rule is in both lists.

A rule with an empty value is allowed. It just never matches anything.

## 4. Examples

| filter | record properties | inherited? |
|---|---|---|
| none | anything | yes |
| `exclude: tier=low` | `tier=low` | no |
| `exclude: tier=low` | `tier=high` | yes (default include) |
| `exclude: tier=low` | none (old record) | yes (default include) |
| `include: tier=high`, `default: exclude` | `tier=high` | yes |
| `include: tier=high`, `default: exclude` | `tier=low` | no |
| `include: tier=high`, `exclude: source=bot` | `tier=high, source=bot` | no (exclude first) |
| `exclude: tier=low` | `tier=Low` | yes (exact match only) |
| `exclude: permanent=true` | old lifetime ban, no map | no (derived) |
| `minimum_duration_seconds: 3600`, `exclude: tier=low` | permanent, `tier=low` | no |
