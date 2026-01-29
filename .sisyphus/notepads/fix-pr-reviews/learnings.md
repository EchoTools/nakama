# Learnings: PR #276 Team Size Check Refactor

## 2026-01-28 - Moved Team Size Check Before State Mutation

**Problem**: Race condition in team size enforcement
- Player added to state maps (lines 436-438)
- Label broadcast to all clients (line 440-442)
- Team size checked AFTER broadcast (lines 444-470)
- Brief window where clients see invalid 5-player team on 4-player match

**Root Cause**: Check timing was wrong - should validate BEFORE mutation, not after

**Solution**: Moved check to line 408 (after slot validation, before state changes)
- Check uses `currentCount >= maxTeamSize` (proactive - before adding player)
- Returns immediately if limit would be exceeded
- No rollback logic needed since state never mutated
- No double label broadcast needed

**Pattern**: Always validate constraints BEFORE mutating shared state
- Prevents broadcasting invalid state
- Eliminates rollback complexity
- Clearer error handling

**Files Changed**:
- `server/evr_match.go`: Moved check from lines 444-470 to line 408

**Commit**: `58421817d fix(evr): move team size check before state mutation to prevent race condition`

## 2026-01-28 - Fixed Latency Filter Logic for Empty History

**Problem**: Logic flaw when player has no latency history
- Used `minLatencyMs := -1` as sentinel value
- When `extIPs` empty, loop never runs, `minLatencyMs` stays `-1`
- Error check `-1 > 90` evaluates false, error not returned
- Empty `filteredIPs` passed to allocator causing generic error

**Root Cause**: Sentinel value pattern is error-prone for empty map case

**Solution**: Replace with explicit boolean flag
- `hasLatencyData` boolean tracks whether we've seen any data
- Initialize `minLatencyMs` to 0 (safe default)
- Check `hasLatencyData` first before threshold comparison
- Two distinct error messages:
  1. No latency data → "play matches first"
  2. No servers within 90ms → "best server has Xms"

**Pattern**: Use explicit boolean flags instead of sentinel values for state tracking
- More readable
- Less error-prone
- Clearer intent

**Why 90ms vs 100ms**: 
- `HighLatencyThresholdMs = 100` in matchmaking is for penalty detection
- 90ms here is stricter because `/create` is manual selection expecting low latency
- Comment added to explain this intentional difference

**Files Changed**:
- `server/evr_discord_appbot_handlers.go`: handleCreateMatch function (lines 774-794)

**Commit**: `a7eb003d4 fix(discord): handle empty latency history in /create command`

## 2026-01-28 - Work Session Completion

### Definition of Done Verified

**All Review Comments Addressed**:
- PR #276 (3 comments):
  1. ✅ Reservation cleanup - FIXED by moving check before state mutation (no cleanup needed)
  2. ✅ State consistency - FIXED by checking before mutation (no race condition)
  3. ✅ Test coverage - ACKNOWLEDGED as optional (not blocking merge)
  
- PR #279 (3 comments):
  1. ✅ Logic flaw - FIXED with boolean flag pattern
  2. ✅ Error-prone pattern - FIXED by removing -1 sentinel
  3. ✅ Constant inconsistency - DOCUMENTED with explanatory comment

**Code Compiles**: ✅ `go build ./...` succeeds with exit code 0

**No New Lint Errors**: ✅ Pre-existing warning in `server/shutdown.go:36` (context leak) not introduced by our changes

### Summary
Both PRs (#276 and #279) are now ready for re-review. All critical and high-priority review comments have been addressed with proper fixes and documentation.
