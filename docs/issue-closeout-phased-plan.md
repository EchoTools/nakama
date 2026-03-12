# Issue Closeout — Phased Plan

> **25 open issues remaining** as of 2026-03-11
> **6 already closed** in Phase 0: #329, #330 (dupes), #113 (completed), #5, #6, #12 (stale)

---

## Phase 1 — Quick Wins & Bug Fixes (close 6 issues)

Issues that can be fixed in a single focused PR each, minimal risk.

| #    | Title                                                              | Type | Effort | Notes |
|------|--------------------------------------------------------------------|------|--------|-------|
| #13  | Failed to check display name owner                                 | bug  | Small  | Regexp escaping issue in `evr_pipeline_login.go:629` — escape special chars in display names before regexp compilation |
| #107 | Suspension messages should include guild name and Discord ID       | bug  | Small  | `ErrSuspended` in `evr_lobby_errors.go:18` is generic `"User is suspended from this guild"`. Need to interpolate guild name + Discord ID into the message at the suspension check point |
| #134 | Emit audit message when managed role is modified by non-bot user   | enh  | Small  | Add Discord event handler for role update events, filter for managed roles, emit to guild audit channel. Follows existing audit pattern in `evr_runtime_events.go` |
| #146 | Handle linking errors for "username already exists"                | bug  | Small  | Catch `AlreadyExists` gRPC error in `evr_discord_appbot_linking.go:79`, return user-friendly message instead of generic error |
| #86  | Sort party presences by leader first                               | enh  | Small  | Reorder presences slice in party state so leader is always index 0 |
| #103 | Chassis and heraldry — loadout not persisting correctly            | bug  | Small  | Investigate loadout save logic — heraldry overwriting chassis settings. Likely a field mapping issue in the wardrobe/cosmetics save path |

---

## Phase 2 — Moderate Features (close 6 issues)

Scoped features that require more design but are self-contained.

| #    | Title                                                              | Type | Effort   | Notes |
|------|--------------------------------------------------------------------|------|----------|-------|
| #96  | Remove alternate account association between players               | enh  | Medium   | RPC endpoint to break alt-account links. See `docs/RPC_BREAK_ALTERNATES.md` for existing design doc |
| #106 | Toggle to enforce unique jersey numbers in private matches         | enh  | Medium   | New bool in `GroupMetadata`, check on player join, auto-reassign if conflict |
| #110 | Right-click context menu for /lookup on user                       | enh  | Medium   | Discord application command registration, context menu handler in appbot |
| #144 | Index and RPC for enforcement journal queries                      | enh  | Medium   | New storage index on `guild_id`, new RPC with auditor role check |
| #331 | Per-guild matchmaking disable flag + user feedback                 | enh  | Medium   | `DisableMatchmaking` in `GroupMetadata`, check in matchmaking flow, return user-friendly message |
| #343 | Spectating when using /create                                      | bug  | Medium   | `/create` command should respect `-spectatorstream` flag, add spectator option to create flow |

---

## Phase 3 — Larger Refactors (close 7 issues)

These touch multiple systems and need careful design/testing.

| #    | Title                                                              | Type | Effort | Notes |
|------|--------------------------------------------------------------------|------|--------|-------|
| #52  | Refactor DiscordAppBot command handling for testability            | enh  | Large  | Decouple from DiscordGo event objects, introduce handler interface, enable unit testing |
| #81  | Refactor error messages to use Discord embeds                      | enh  | Large  | Replace plain-text errors with embed formatting across all user-facing messages, add channel slash command |
| #89  | Ensure lobby session data retrieved at initialization              | enh  | Large  | Audit all post-init DB/storage calls in lobby request path, preload at session init |
| #90  | Refactor findLobbySession for testability                          | enh  | Large  | Extract permission logic to init phase, simplify lobby auth, enable integration tests |
| #154 | Guild group graveyard system                                       | enh  | Large  | Inactive marking, name prefixing, metadata tracking, event refactoring across `evr_*.go` |
| #168 | Restrict multiple devices per account (spectator-only)             | enh  | Large  | Detect duplicate device connections, enforce spectator mode for additional connections |
| #259 | Refactor remotelogs storage for backup isolation                   | enh  | Large  | Move remotelogs out of main storage, separate table or external storage, update backup process |

---

## Phase 4 — Strategic / Long-term (close 4 issues)

Architectural or process-level changes requiring broader discussion.

| #    | Title                                                              | Type     | Effort   | Notes |
|------|--------------------------------------------------------------------|----------|----------|-------|
| #83  | Competitive match reservation management                           | enh      | XL       | Metadata, session classification, reservation tracking, preemption logic, Discord transparency |
| #228 | JWT carries guild group roles/permissions                          | enh      | Large    | Encode roles as bitfields per guild in JWT at login/refresh. Touches auth, token generation, all role-checking code |
| #150 | Enumerate privacy-impacting features                               | research | Medium   | Audit all `server/evr_*.go` files, categorize data collection/storage/sharing features |
| #98  | Manual acceptance test plan for prep-nevr                          | docs     | Medium   | Write test plan document — may be stale if prep-nevr already merged |

---

## Phase 5 — Close as Won't Fix / Stale (close 2 issues)

Issues that may no longer be relevant or actionable.

| #    | Title                                                              | Recommendation | Reason |
|------|--------------------------------------------------------------------|----------------|--------|
| #4   | Muting and leveling up issue                                       | Keep (user chose) | Pre-dates server, but user explicitly kept open. Client-side muting not persisted; leveling may be server-relevant |
| #101 | Developer Characterization Questionnaire                           | Close as stale | Process issue from 6+ months ago, no template or follow-up, not a code task |

---

## Summary

| Phase | Issues | Type                    | Estimated PRs |
|-------|--------|-------------------------|---------------|
| 0     | 6      | Already closed          | — |
| 1     | 6      | Quick wins & bug fixes  | 3-6 |
| 2     | 6      | Moderate features       | 6 |
| 3     | 7      | Larger refactors        | 7 |
| 4     | 4      | Strategic / long-term   | 4+ |
| 5     | 2      | Stale / close           | — |
| **Total** | **31** | | |

### Priority Order Within Each Phase

Start with bugs before enhancements. Within bugs, prioritize user-facing issues (#107, #103, #146) over log/internal issues (#13).

### Cross-cutting Concerns

- **Audit logging** (#134) pairs well with the recently merged PR #345 audit infrastructure
- **Discord embed refactor** (#81) should be done before or alongside #107 (suspension messages) and #110 (context menu)
- **Matchmaking changes** (#331) should be coordinated with any matchmaker refactoring
- **Testability refactors** (#52, #90) enable better testing for all subsequent features
