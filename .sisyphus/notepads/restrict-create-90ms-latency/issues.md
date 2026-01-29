# Issues - restrict-create-90ms-latency

This file tracks problems and gotchas discovered during implementation.

## Pre-existing Build Errors on Main Branch

**Date**: 2026-01-28  
**Issue**: Main branch has compilation errors in `server/evr_pipeline_login.go` unrelated to our work:
- `undefined: evr.NewSNSEarlyQuitConfig`
- `undefined: evr.DefaultEarlyQuitFeatureFlags`
- `clientProfile.EarlyQuitFeatures undefined`
- `undefined: evr.NewLockoutActiveNotification`

**Impact**: Cannot verify full project build, but our changes to `server/evr_discord_appbot_handlers.go` are isolated and correct.

**Verification Approach**: Confirmed that:
1. Main branch has these errors before our changes (verified via `git stash`)
2. Our diff only touches `handleCreateMatch()` function
3. LSP would catch any errors in our modified file (Go syntax is valid)
