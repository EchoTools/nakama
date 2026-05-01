FACT:
- `bigduckii` is `uid=ecc390b5-2cc9-48c8-9f4a-876425a494a2`, `evrid=OVR-ORG-18764`.
- In the last 24h log, `bigduckii` repeatedly queues in party group `duck10` with leader `qvinoo.` (`uid=42894aad-f87a-4de1-954d-44f7c281502a`, `evrid=OVR-ORG-15327`).
- In both traced `duck10` arena queues, the leader path logs `Party is ready` with `size":2,"members":["bigduckii"]`, then `Finding match ... "party_size":2`.
- In both traced `duck10` arena queues, `bigduckii` does not stay on that leader path. Her session logs:
  - `User is member of party, attempting to follow leader`
  - `Leader is currently matchmaking, falling through`
  - `Polling to follow party leader (follower is in a lobby)`
- Later, the follower path accepts tracker-state convergence as success via:
  - `Context canceled during settle but follower is in leader's match`
- Code path:
  - `server/evr_lobby_find.go`: `TryFollowPartyLeader()` returns `false` when the leader still has a matchmaking stream.
  - `server/evr_lobby_find.go`: because the follower is already in a lobby, that fallthrough becomes `pollFollowPartyLeader()`.
  - `server/evr_lobby_find.go`: `pollFollowPartyLeader()` can then return success based on service-stream match equality alone.
  - `server/evr_lobby_joinentrant.go`: service-stream state is updated before the client transition is necessarily complete.

INFERENCE:
- The split starts before the final join attempt: followers are being diverted out of the leader’s shared party-ticket flow and into a separate follow/poll flow exactly when the leader is already matchmaking.
- That follow/poll flow is race-prone because it treats service-stream match convergence as completion, which can happen before the client has actually left the social lobby and finished the new match load.

HYPOTHESIS:
- The “one party member enters, others stay in social lobby” symptom is caused by followers missing or stalling during that follow/poll handoff after the leader’s ticket has already advanced.
- The fix is likely either:
  1. keep party followers on the same party matchmaking path while the leader is matchmaking, instead of forcing them into follow/poll, or
  2. tighten the follower success condition so it requires a stronger completion signal than `StreamLabelMatchService` equality alone.

Saved supporting notes/scripts in `_investigation/`:
- `_investigation/bigduckii_party_split_2026-04-22.md`
- `_investigation/extract_bigduckii_party_split.sh`
