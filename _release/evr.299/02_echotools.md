## Release Notes v3.27.2-evr.299

Fixes a race condition in the party follow system that caused false-positive placement detection.

### Party System

* **Party Follow Fix:** Fixed a bug where party members could get separated from their leader when entering a match. The server was incorrectly detecting follower placement before the join completed.

### Technical / Internal

* **pollFollowPartyLeader Race Condition:** The `isFollowerInLeaderMatch` closure and the poll loop's convergence check both relied solely on tracker stream status to determine whether a follower had joined the leader's match. The tracker can converge before the client completes `lobbyJoin`, producing a false positive. Now both paths verify the follower appears in the match's player list via `MatchLabelByID` + `GetPlayerByUserID` before declaring success. Affected file: `server/evr_lobby_find.go`.

-# [Full Changelog](https://discord.com/channels/1179666360863817749/1485036740820472080)