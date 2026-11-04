# Combat team sort

Status: implemented in #775.

## What it does

A public combat match is 4v4. The matchmaker packs the players (`groupEntriesSequentially`),
predicts and assembles as it always has, and then, for each made combat match only,
`sortCombatMatches` (`server/evr_matchmaker_combat.go`) re-sorts **the same players**
onto the two teams OpenSkill rates closest to a draw. Arena never reaches this code.

There is no quality gate for combat (#479 stands) and no candidate selection beyond the
packing that already exists. Reservation, widening, release and the failsafe are unchanged.

## The split

- All splits are tried: 35 for 8 players, 6,435 for 16 (exhaustive). For two teams
  `rating.PredictDraw` depends on the split only through |sum(mu) difference|, so the
  closest-to-draw split is the smallest difference.
- Parties (a ticket with more than one player) are kept together if the best split that
  keeps every party together is within `combat_draw_tolerance` of the closest-to-draw
  split's draw probability.
- Otherwise every split is scored `|dmu| + combat_split_penalty_weight x sum(recent-split
  score of each player moved away from their party)` and the lowest wins. For a split
  party, the side that makes the sum smallest is its "home"; the members off it are the
  ones moved. With equal scores the larger group, then the lowest session id's side, is home.
- Players are ordered by session id first, and the colour is alternated by a hash of
  the players, so the result does not depend on arrival order.

## Recent-split score

Per player, in storage `Matchmaker` / `combat_splits` (`CombatSplitHistory`), next to
`config` (MatchmakingSettings) and `join` (JoinDirective). A party member's score is
multiplied by 0.8 each time they are sorted into a made combat match with a party, then
gains 1 if they were the one moved. Written only when it changes, for matches that stand.
Players with no record score 0. Solo players have no record. A score is read and decayed
only in matches that contain a party for that player.

## Settings (`GlobalMatchmakingSettings`)

| key | default | meaning |
|---|---|---|
| `combat_draw_tolerance` | 0.003 | draw probability a party-keeping split may fall short by (about 3.5 mu of team-sum difference at the default sigma) |
| `combat_split_penalty_weight` | 1.0 | mu points per unit of a moved player's recent-split score |

Zero means the default, as for the other float settings in that struct.

## Ratings

Every player always has a combat rating: the combat-mode `TeamSkillRating`, or the default
mu/sigma when there is no record. No arena fallback.

A ticket carries `rating_mu` / `rating_sigma` only when `enable_skill_based_mm` is on
(`evr_lobby_parameters.go`); that is not changed. For a player whose ticket carries none,
the sort loads the rating at sort time with the ticket's own loader, `MatchmakingRatingLoad`
for the combat mode, and uses the default when there is no record or the read fails
(Warn). Tickets that carry a rating are not loaded again. The sort therefore does not depend on
`enable_skill_based_mm`.

## Logging

One Info record per sorted match, `Combat teams sorted`: `user_ids`, `parties`,
`kept_parties`, `fallback`, `moved`, `draw_before`, `draw_after`, `draw_best`, `draw_kept`,
`cost`, `splits`, `tolerance`, `weight`.

## Builder

`repairSplitTicketTeams` reunites a split party in `buildMatch`; it is skipped for combat
(`repairTeamsForMode`), so the sorted teams are the teams. Arena is unchanged.

## Combat result

`processPostMatchMessages` reads a combat player's result from `CombatWins` / `CombatLosses`
(then the arena keys), so a combat win moves the winners' `TeamSkillRating` up.
