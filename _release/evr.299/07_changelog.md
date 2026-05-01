## Changelog v3.27.2-evr.299

### Party System

* **Party Follow Race Condition Fix:** Fixed `pollFollowPartyLeader` falsely marking followers as placed in the leader's match. The `isFollowerInLeaderMatch` closure and the poll loop's convergence check both relied solely on tracker stream status, which can converge before the client completes the join. Both paths now verify the follower appears in the match's player list via `MatchLabelByID` before declaring success. Fixes party splits where the leader entered a match but followers stayed in the social lobby.