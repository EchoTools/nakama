# Early Quit Logout Strike Implementation

## Feature Summary
When recording an early quit in the game, a goroutine function now checks if the player subsequently logs out of the system. If they do, the early quit penalty is removed from their record since logging out confirms they actually quit the game entirely (not just that match).

## Implementation Details

### File: `server/evr_earlyquit.go`

**New Function: `CheckAndStrikeEarlyQuitIfLoggedOut`**
- Type: Goroutine-compatible function
- Parameters:
  - `ctx context.Context`: For cancellation and logging context
  - `logger runtime.Logger`: For structured logging
  - `nk runtime.NakamaModule`: Nakama runtime for storage operations
  - `sessionRegistry SessionRegistry`: To check active sessions
  - `userID string`: User UUID as string
  - `sessionID string`: Session UUID that disconnected
  - `checkInterval time.Duration`: Grace period before checking logout status

- Behavior:
  1. Waits for `checkInterval` (grace period) before checking logout status
  2. Checks if the player's session still exists in sessionRegistry
  3. If session exists: Does nothing (player still active)
  4. If session doesn't exist: Loads player's EarlyQuitConfig and calls `IncrementCompletedMatches()` to reduce penalty
  5. Writes updated config back to storage
  6. Logs the action

- Key Design:
  - Non-blocking: Runs as a goroutine to avoid blocking match logic
  - Context-aware: Respects context cancellation
  - Safe: Loads config fresh from storage before modifying
  - Configurable: Grace period can be adjusted (currently 5 minutes)

### File: `server/evr_match.go`

**Integration Point: MatchLeave function (line ~675)**
- After recording early quit penalty and writing to storage
- Called within the success case after `StorableWrite`
- Grace period set to 5 minutes (300 seconds)

Goroutine call:
```go
go CheckAndStrikeEarlyQuitIfLoggedOut(ctx, logger, nk, _nk.sessionRegistry, 
                                     mp.GetUserId(), mp.GetSessionId(), 5*time.Minute)
```

### Imports Added
- `context` - For context parameter
- `github.com/gofrs/uuid/v5` - For UUID parsing

## How It Works

1. Player early quits match → Penalty recorded immediately
2. 5-minute timer starts in background goroutine
3. After 5 minutes:
   - Check if player's session still exists
   - If gone: Remove penalty by treating as completed match
   - If still exists: Keep penalty (player still in system)

## Configuration Notes
- Grace period: 5 minutes (adjustable by changing `5*time.Minute` constant)
- Uses `IncrementCompletedMatches()` which decreases penalty by 1
- Only removes penalty for the ONE early quit (one completed match = one penalty reduction)
- Future: Could be made configurable via ServiceSettings

## Testing Considerations
- Verify goroutine completes without blocking
- Test with player who logs out within grace period
- Test with player who stays logged in (penalty should remain)
- Check storage is updated correctly
- Verify logging shows correct penalty levels before/after
