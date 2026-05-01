# bigduckii party split investigation

Date: 2026-04-22 UTC
Scope: `/var/tmp/nakama-logs/nakama.log` from the last 24 hours, `server/evr_lobby_find.go`, `server/evr_lobby_joinentrant.go`, `server/evr_lobby_group.go`, `server/party_handler.go`

## Identity

- `bigduckii` user ID: `ecc390b5-2cc9-48c8-9f4a-876425a494a2`
- `bigduckii` EVR ID: `OVR-ORG-18764`
- `bigduckii` login success: `2026-04-22T17:13:51.332Z`
- Party leader in the traced `duck10` transitions: `qvinoo.` / `42894aad-f87a-4de1-954d-44f7c281502a` / `OVR-ORG-15327`

## Direct log evidence

Identity:

> `218797: ... "uid":"ecc390b5-2cc9-48c8-9f4a-876425a494a2","evrid":"OVR-ORG-18764","username":"bigduckii","message":"SNSLogInSuccess(session=a20fc04b-3e6e-11f1-a9b2-383ec87527a1, user_id=OVR-ORG-18764)"}`

### `duck10` party behavior repeats for bigduckii

First traced arena queue:

> `223805: ... "msg":"Joined party group" ... "username":"bigduckii" ... "partyGroupName":"duck10"}`

> `223806: ... "msg":"User is member of party, attempting to follow leader" ... "username":"bigduckii" ... "leader":"qvinoo."}`

> `223807: ... "msg":"Leader is currently matchmaking, falling through" ... "username":"bigduckii"}`

> `223808: ... "msg":"Polling to follow party leader (follower is in a lobby)" ... "username":"bigduckii"}`

At the same moment the leader had the full party ticket:

> `223812: ... "msg":"Party is ready" ... "username":"qvinoo." ... "size":2,"members":["bigduckii"]}`

> `223813: ... "msg":"Finding match" ... "username":"qvinoo." ... "mode":"echo_arena","party_size":2}`

Second traced arena queue shows the same pattern again:

> `275417: ... "msg":"Joined party group" ... "username":"bigduckii" ... "partyGroupName":"duck10"}`

> `275418: ... "msg":"User is member of party, attempting to follow leader" ... "username":"bigduckii" ... "leader":"qvinoo."}`

> `275419: ... "msg":"Leader is currently matchmaking, falling through" ... "username":"bigduckii"}`

> `275420: ... "msg":"Polling to follow party leader (follower is in a lobby)" ... "username":"bigduckii"}`

Leader again built the party ticket as size 2:

> `275405: ... "msg":"Party is ready" ... "username":"qvinoo." ... "size":2,"members":["bigduckii"]}`

> `275406: ... "msg":"Finding match" ... "username":"qvinoo." ... "mode":"echo_arena","party_size":2}`

### Earlier split-window evidence in the same investigation draft

The follower path later decided success from tracker state alone:

> `275978: ... "msg":"Matchmaking stream closed, canceling matchmaking" ... "username":"bigduckii"}`

> `275979: ... "msg":"Context canceled during settle but follower is in leader's match" ... "username":"bigduckii"}`

That proves the follower branch is willing to declare success based on service-stream convergence, not on a completed client transition.

## Relevant code

`server/evr_lobby_group.go`
- `JoinPartyGroup()` joins the shared party group and returns `isLeader`.

`server/evr_lobby_find.go`
- `configureParty()` only sets `party_size` on the leader path after `Party is ready`.
- Non-leaders are returned with shared `memberSessionIDs`, but they do not drive the party ticket themselves.
- `TryFollowPartyLeader()` explicitly short-circuits when the leader has a matchmaking stream:
  - log text: `Leader is currently matchmaking, falling through`
  - return value: `false`
- Because the follower is already in a lobby, the fallback path becomes `pollFollowPartyLeader()`.
- `pollFollowPartyLeader()` can still return success later if tracker `StreamLabelMatchService` says follower and leader point at the same match:
  - log text: `Context canceled during settle but follower is in leader's match`

`server/evr_lobby_joinentrant.go`
- The join path updates service-stream state before the client has necessarily completed leaving the old lobby and finishing the new load.
- That ordering is enough for the matchmaking-stream monitor to cancel the follower flow early.

`server/party_handler.go`
- `JoinRequest()` auto-joins open parties, so `bigduckii` is genuinely a member of the leader's party.
- The split is not caused by the party membership failing to form.

## Findings

FACT:
- `bigduckii` is `uid=ecc390b5-2cc9-48c8-9f4a-876425a494a2`, `evrid=OVR-ORG-18764`.
- In both traced `duck10` arena queue attempts, `qvinoo.` queued as leader with `party_size=2` and member list `["bigduckii"]`.
- In both traced `duck10` arena queue attempts, `bigduckii` did not enter the leader queue path. She entered the follower path, logged `Leader is currently matchmaking, falling through`, then switched to `Polling to follow party leader (follower is in a lobby)`.
- The follower path later accepts tracker-state convergence as success: `Context canceled during settle but follower is in leader's match`.
- The code that produces those exact log lines is in `server/evr_lobby_find.go`.

INFERENCE:
- The split condition starts before the final join. Followers are not consistently riding the leader’s party matchmaking ticket; they are treated as separate followers that must poll and catch up after the leader is already matchmaking.
- That follower/poll path is race-prone because it can mark success from `StreamLabelMatchService` equality before the client has fully completed the transition out of the old lobby.

HYPOTHESIS:
- The user-facing “one player enters, others stay in social lobby” symptom happens when the leader’s party ticket progresses but one or more followers remain in the poll/catch-up branch long enough to miss or stall the handoff.
- The durable fix is likely to stop falling followers out of the shared party-queue flow when the leader is already matchmaking, or to require a stronger completion signal than service-stream match equality before the follower path returns success.
