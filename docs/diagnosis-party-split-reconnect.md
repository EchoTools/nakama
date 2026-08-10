# Diagnosis: party members split off when they drop & reconnect

Source investigation: `echovrce-ops/cases/2026-06-30-coastermaster77-party-split/`
(coastermaster77, Discord 770665684824489995). Logs showed 34 party-full + 14
lobby-full lockouts over 72h, 45 sessions, reconnect storms of 24 and 14
failures. This file records the **code-level** root cause + the fix plan.

## The player-visible symptom

A player in a party drops (EVR sessions drop constantly), reconnects seconds
later, and cannot get back in with their friends:

```
failed to join party: server is full: party is full   (evr_lobby_find.go:383)
failed to join match: server is full: ... lobby full   (join to the full 12-slot social lobby)
```

→ bounced to lobby-select (`LobbySessionFailurev4`), retries a new session,
fails again. The party is "split."

## Root cause (code)

Two contributing defects, both in the party join path:

### 1. Full-check precedes identity-check — `server/party_handler.go:113-120`

```go
func (p *PartyHandler) JoinRequest(presence *Presence) (bool, error) {
	...
	// Check if party is full.
	if p.members.Size() >= p.MaxSize {          // <-- fires first
		return false, runtime.ErrPartyFull
	}
	if p.Open { ... p.members.Join(...) ... }   // EVR parties are Open
	...
	// Check if party already has this user.                 <-- unreachable for Open parties
	for _, member := range p.members.presences {
		if member.Presence.UserID == presence.UserID { return false, ErrPartyJoinRequestAlreadyMember }
	}
```

The cap is enforced before the party ever checks whether this UserID is already
(or was just) a member. For `Open` parties the already-member branch is dead
code. A member who is reconnecting is treated as a brand-new fifth joiner.

### 2. No slot reservation for a dropped member — `JoinPartyGroup`, `server/evr_lobby_group.go:148`

```go
ph, created, err := session.pipeline.partyRegistry.GetOrCreateByGroupName(groupName, true, 4, userPresence)
```

`MaxSize` is a hardcoded `4`. When a member's session dies, their party-stream
presence is untracked and the slot is **immediately** reclaimable. Between the
drop and the reconnect the slot gets backfilled (another friend, or matchmaking),
so the returning member — even the party's own founder — is now the 5th and is
rejected. There is no grace window in which a recently-departed member can
reclaim their seat.

## Fix plan (implement on this branch, unit-tested; deploy is Andrew's call)

1. **Reorder `JoinRequest`**: check "is this UserID already a member?" *before*
   the full-check. An existing member re-requesting is idempotent success
   (return `ErrPartyJoinRequestAlreadyMember` semantics / no-op), never
   `ErrPartyFull`. Surgical, single-function, covered by a new unit test.
2. **Reconnect reservation (grace window)**: when a member leaves, record
   `{userID → departedAt}`; hold their seat for a short grace (config'd, not
   hardcoded). During the window, `Size()` counts the reservation so backfill
   can't steal it, and a returning member with a matching UserID reclaims it
   without tripping the cap. Reservation expires after the grace so genuinely
   gone members free the seat.
3. **Un-hardcode `MaxSize`** (the literal `4`): promote to a named config so the
   party cap is auditable and tunable (no magic numbers). Keep default = 4;
   this is not a cap change, just removing the literal.

### Constraints honored (per AGENTS.md)
- Single subsystem (party handler); no matchmaker-wide surgery.
- Do NOT touch `GroupID` guild isolation.
- Every logic change ships with tests; `TryFollowPartyLeader`/
  `pollFollowPartyLeader` already have 30+ tests — none may regress.
- Full gate before commit: `gofmt`, `go vet`, `golangci-lint`,
  `go test -race ./server/...`, `go mod tidy`, `govulncheck`.
- **Not for unattended deploy.** Matchmaker changes need integration tests on a
  cluster; land tested on this branch, Andrew reviews + deploys off-peak.

---

## Verification note, added 2026-08-10 on rehoming

This document was written on a branch that carried no implementation, and it sat
unreviewed until a branch audit found it. Every code claim above was re-checked
against `main` at `1b3820fc6` before it was moved here. **All three still hold**;
only line numbers had drifted:

| claim | then | now |
|---|---|---|
| full-check precedes identity-check in `JoinRequest` | `party_handler.go:113-120` | `party_handler.go:115-120` |
| party `MaxSize` hardcoded to 4 | `evr_lobby_group.go:148` | `evr_lobby_group.go:161` |
| `ErrPartyFull` surfaces as "party is full" | `evr_lobby_find.go:383` | `evr_lobby_find.go:381` |

None of the fix plan has been implemented: no reorder, no grace window, no named
constant for the party size.

### Do not mistake the existing membership check for the fix

`JoinPartyGroup` (`evr_lobby_group.go:170-191`) walks `ph.members.List()` and
skips `JoinRequest` when it finds a matching `UserId`. It is tempting to read
that as already solving this. It is not the fix, and it is worth being precise
about why, because two careful readings of this code reached opposite
conclusions during the audit:

- It only fires while a presence for that user is **still listed**. Session death
  drives `PartyHandler.Leave` → `members.Leave`, which removes it.
- Everything below that check is **SessionID**-keyed, including
  `PartyPresenceList`'s `presenceMap`/`reservedMap`. A reconnecting player has a
  new session ID and is a new occupant at that layer. The `UserId` comparison is
  advisory: it gates a call, it reserves nothing.

**The disagreement is not resolvable by reading.** It is resolvable by a test,
and the reason it went unnoticed is a coverage gap:
`TestPartyHandler_JoinRequestAlreadyMember` (`evr_party_system_test.go:628`)
constructs the handler with `open=false`, while EVR only ever creates parties
with `open=true` (`evr_lobby_group.go:161`). The Open-party path — the only one
that runs in production — has no test at all.

**Write that test first.** It decides whether the rest of this document
describes a live defect or a latent one.
