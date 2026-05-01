# echotools/nakama#406 investigation

Source log: `/var/tmp/nakama-logs/nakama.log`

Window used: last 24h only, relative to `2026-04-22`.

## Identities

- FACT: `bigduckii` is `ecc390b5-2cc9-48c8-9f4a-876425a494a2`, EVR ID `OVR-ORG-18764`, Discord ID `689318148520149055`.
- FACT: `qvinoo.` is `42894aad-f87a-4de1-954d-44f7c281502a`, EVR ID `OVR-ORG-15327`.
- FACT: `lyric0507` is `4d47ad8d-3f28-47f7-86bd-b3f57f9f803c`, EVR ID `OVR-ORG-4637350426375315`.

## Evidence timeline

### 1. Initial `duck10` formation was undersized

- FACT: `2026-04-22T17:04:41.717Z` `qvinoo.` used the Discord `/party group group-name:duck10` command.
- FACT: `2026-04-22T17:08:53.357Z` `qvinoo.` joined party group `duck10` as runtime party `f6897335-1cb8-4634-a8cb-b2de7cab8107.nakama2_us-east`.
- FACT: The immediately following leader log shows the runtime party as undersized:
  - `2026-04-22T17:08:53.357Z` `Party is ready ... leader=f07ff68f-3e6d-11f1-a9b2-383ec87527a1 size=0 members=[]`
- FACT: `2026-04-22T17:08:54.754Z` `qvinoo.` entered social match `4445136d-be66-4385-8bf2-4dd79bc10011`.
- FACT: `2026-04-22T17:14:06.739Z` `bigduckii` then joined that same party instance:
  - `Joined party group ... partyID=f6897335-1cb8-4634-a8cb-b2de7cab8107.nakama2_us-east`
  - `User is member of party, attempting to follow leader`
  - `Joining leader's lobby ... mid=4445136d-be66-4385-8bf2-4dd79bc10011.nakama2_us-east`
- FACT: `2026-04-22T17:14:07.352Z` `bigduckii` entered the same social lobby.

### 2. Both players crashed out and re-formed `duck10`

- FACT: `2026-04-22T17:21:44.984Z` `bigduckii` disconnected from social `4445136d-be66-4385-8bf2-4dd79bc10011`:
  - `Closed client connection ... username="bigduckii"`
  - `Player leaving the match ... reason=4`
- FACT: `2026-04-22T17:21:44.994Z` reconnect reservation was created for `bigduckii`.
- FACT: `2026-04-22T17:21:46.395Z` `qvinoo.` disconnected from the same social:
  - `Closed client connection ... username="qvinoo."`
  - `Player leaving the match ... reason=4`
- FACT: `2026-04-22T17:21:46.402Z` reconnect reservation was created for `qvinoo.`.
- FACT: After reconnect, `duck10` became a new runtime party ID:
  - `2026-04-22T17:22:08.111Z` `qvinoo.` joined `ef8b913a-17fd-49a1-9075-781faf7547cc.nakama2_us-east`
  - `2026-04-22T17:22:08.111Z` immediate leader log still showed `size=0 members=[]`
  - `2026-04-22T17:22:35.622Z` `bigduckii` joined the same runtime party
  - `2026-04-22T17:22:35.931Z` leader log now showed `size=2 members=["bigduckii"]`

### 3. Once the runtime party contained both players, matchmaking stayed atomic

- FACT: `2026-04-22T17:22:38.950Z` `Matchmaking ticket added` for `qvinoo.` used ticket `e4eaa616-7467-47ab-ac8b-5e419c893af4` and its `presences` included `bigduckii` session `ca375f1e-3e6f-11f1-a9b2-383ec87527a1`.
- FACT: `2026-04-22T17:23:56.123Z` both `qvinoo.` and `bigduckii` logged `Player joining the match.` for arena `1fdccb5d-6df5-4168-bded-8edc855803a4`.
- FACT: `2026-04-22T17:23:57.275Z` `Match built.` for `1fdccb5d-6df5-4168-bded-8edc855803a4` lists `qvinoo.` and `bigduckii` on the same ticket lineage and same `party_id:"ef8b913a-17fd-49a1-9075-781faf7547cc.nakama2_us-east"`.
- FACT: The pattern repeated later:
  - `2026-04-22T17:32:58.942Z` ticket `ba4d25fc-5c76-4cd7-b7f7-d71e66416933` again includes `bigduckii` in `presences`
  - `2026-04-22T17:33:27.289Z` `Match built.` for `3a580260-d076-42b0-975c-8378f8b38e92` again lists both users under the same `party_id`

### 4. A separate follow-release path can also split them after placement

- FACT: `2026-04-22T17:29:44.213Z` `bigduckii` re-entered lobby find while still in `duck10`:
  - `Joined party group ... partyID=ef8b913a-17fd-49a1-9075-781faf7547cc.nakama2_us-east`
  - `User is member of party, attempting to follow leader`
- FACT: The leader's arena was treated as non-joinable:
  - `2026-04-22T17:29:44.213Z` `Leader's match is full or closed ... open=true open_slots=0 party_size=2 required_slots=1`
  - `2026-04-22T17:29:50.215Z` `Leader's non-social match is persistently non-joinable, releasing follower`
  - `2026-04-22T17:29:56.216Z` `Follower cannot join leader's match, redirecting to social lobby`
- FACT: The social fallback itself failed:
  - `2026-04-22T17:29:57.220Z` `Unexpected error while finding match ... failed to join existing social lobby: failed to get entrant metadata`
  - `2026-04-22T17:29:57.535Z` `LobbySessionFailurev4(... msg=failed to join existing social lobby: failed to get entrant metadata)`

## Relevant code

### Party ticket formation

- FACT: [server/evr_lobby_matchmake.go](/home/metis/src/nakama/server/evr_lobby_matchmake.go:307) only uses `lobbyGroup.MatchmakerAdd(...)` when `lobbyGroup != nil && lobbyGroup.Size() > 1`.
- FACT: Otherwise [server/evr_lobby_matchmake.go](/home/metis/src/nakama/server/evr_lobby_matchmake.go:323) submits a solo ticket.
- FACT: [server/party_handler.go](/home/metis/src/nakama/server/party_handler.go:571) snapshots `p.members.List()` into the party ticket presences at ticket-submit time.
- FACT: [server/evr_lobby_find.go](/home/metis/src/nakama/server/evr_lobby_find.go:323) logs `Party is ready` using the current `lobbyGroup.Size()` and member list.

### Follower release after the leader is already in a full arena

- FACT: [server/evr_lobby_find.go](/home/metis/src/nakama/server/evr_lobby_find.go:845) refuses immediate join if the leader match is closed or lacks required slots.
- FACT: [server/evr_lobby_find.go](/home/metis/src/nakama/server/evr_lobby_find.go:955) sets `maxNonJoinableCycles = 1`.
- FACT: [server/evr_lobby_find.go](/home/metis/src/nakama/server/evr_lobby_find.go:1075) logs `Leader's non-social match is persistently non-joinable, releasing follower`.
- FACT: [server/evr_lobby_find.go](/home/metis/src/nakama/server/evr_lobby_find.go:114) redirects the follower to social.

## Conclusions

- INFERENCE: I do not see evidence that a valid two-player `qvinoo.` + `bigduckii` party ticket was split by the matchmaker. When the party handler had both members, the submitted ticket included both and the built matches kept them together.
- INFERENCE: I do see evidence that the leader can enter lobby/match flow while the runtime party snapshot is still undersized. In that state the leader-side logs show `size:0 members:[]`, and the code path would submit solo if matchmaking happened at that moment.
- INFERENCE: I also see a second split-producing path after placement: if a follower retries while the leader is already in a full arena, `pollFollowPartyLeader` intentionally gives up after one non-joinable cycle and redirects the follower to social.
- HYPOTHESIS: The recurring user report is likely a combination of party re-formation timing and reconnect timing rather than a single pure matchmaker bug.
- HYPOTHESIS: The most likely trigger window is after crashes/reconnects or staggered queue timing, when the runtime party UUID has been recreated and the leader reaches find/matchmaking before the follower has been added to the in-memory party membership snapshot.
- HYPOTHESIS: `failed to join existing social lobby: failed to get entrant metadata` is a separate fallback-path bug that worsens recovery after the intentional follower release.

## Saved investigation helpers

- Script: [trace_party_timeline.py](/home/metis/src/nakama/_investigation/trace_party_timeline.py)
- Script: [extract_match_evidence.py](/home/metis/src/nakama/_investigation/extract_match_evidence.py)
- Issue draft: [echotools_nakama_406_comment.md](/home/metis/src/nakama/_investigation/echotools_nakama_406_comment.md)
