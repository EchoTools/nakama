# RPC Cleanup and Refactoring Tickets

This document tracks cleanup and refactoring work identified during the RPC authorization middleware implementation.

## Cleanup Ticket: RPC Endpoint Consolidation and Removal

### Remove Deprecated/Unused Endpoints
- [ ] **Remove `matchmaker/candidates` endpoint** - No longer needed
- [ ] **Remove `importloadouts` RPC** - Legacy code
- [ ] **Remove `match` RPC** - Replaced by built-in Nakama ListMatches
- [ ] **Remove telemetry stream RPCs** (`/telemetry/stream/join`, `/telemetry/stream/leave`) - Handled by standalone system

### Consolidate Duplicate Endpoints
- [ ] **Investigate and resolve `matchmaker/config` vs `matchmaking/settings`** - Determine if these are duplicates
- [ ] **Combine `match/prepare`, `match/allocate`, and `match/build`** - These appear to do the same thing, consolidate into single endpoint
- [ ] **Combine `server/score` and `server/scores`** - Merge into single endpoint with algorithm selection parameter

### Validate Deprecated Code
- [ ] **Verify `signin/discord` is handled by socialauth** - Check if this is now redundant
- [ ] **Verify `link/usernamedevice` is still needed** - Check usage and potentially remove

## Refactoring Ticket: Device Linking Integration

**Goal**: Move device linking/unlinking into BeforeHook for LinkDevice

### Requirements
- [ ] Remove RPCs: `link/device`, `link/usernamedevice`, `link`
- [ ] Integrate device linking into BeforeHook for LinkDevice
- [ ] Allow global operators to unlink devices
- [ ] Add audit messages for all links/unlinks
- [ ] Ensure proper authorization (users link their own, operators can link/unlink any)

## Enhancement Ticket: Role-Based Authorization Middleware

**Goal**: Extend middleware to support role-based authorization patterns

### Implementation
- [ ] Pull in PR that integrates roles into session JWT token
- [ ] Extend RPCPermission to support role checks
- [ ] Add support for common role patterns:
  - Allocator role (for match/prepare)
  - Auditor role (for player/setnextmatch and other RPCs)
  - Enforcer role (for enforcement actions)
  - Moderator role (for matchlock)
  - Match creator role (for match/terminate, player/kick)
  - Server owner role (for match/terminate, player/kick)

### RPCs Requiring Role-Based Auth
- [ ] `match/prepare` - Global ops + allocator role
- [ ] `player/setnextmatch` - Players (own), auditors, enforcers
- [ ] `player/kick` - Global ops, guild enforcers, match creator, server owner
- [ ] `player/matchlock` - Global ops, guild moderators
- [ ] `match/terminate` - Global ops, guild enforcers, match creator, server owner

## Enhancement Ticket: Guild-Based Authorization Middleware

**Goal**: Add middleware support for guild-based permissions

### Implementation
- [ ] Add performant function for checking shared guild membership
  - Custom PostgreSQL query for group edge validation
  - Optimize for use as post-filter in multiple RPCs
- [ ] Add guild ownership check to middleware
- [ ] Add guild role checks (enforcer, moderator, owner)

### RPCs Requiring Guild-Based Auth
- [ ] `account/lookup` - Authenticated users who share a guild
- [ ] `account/search` - Authenticated users, filter by shared guild (in RPC)
- [ ] `guildgroup` - Guild owner + global operators
- [ ] `enforcement/kick` - Global ops + guild enforcers
- [ ] `enforcement/journals` - Global ops + guild owners
- [ ] `enforcement/record/edit` - Global ops + guild enforcers

## Enhancement Ticket: Owner-Based Authorization Middleware

**Goal**: Add middleware support for resource ownership validation

### Implementation
- [ ] Extend RPCPermission to support owner checks
- [ ] Add pattern for "user accessing own resource OR operator"
- [ ] Implement for outfit RPCs and other user-scoped resources

### RPCs Requiring Owner Auth
- [ ] `player/outfit/save` - Owner only
- [ ] `player/outfit/list` - Owner only
- [ ] `player/outfit/load` - Owner only
- [ ] `player/outfit/delete` - Owner only

## Enhancement Ticket: Enforcement Journal Refactoring

**Goal**: Refactor enforcement journal RPCs into logical idiomatic pattern

### Requirements
- [ ] Combine enforcement journal RPCs in a logical pattern
- [ ] Add new field to distinguish kicks from other enforcement actions
- [ ] Kicks should be recorded in enforcement journal
- [ ] Kicks should NOT be displayed in `/lookup` or `/whoami`
- [ ] Update related RPCs and data structures

## Enhancement Ticket: Early Quit History Storage Permissions

**Goal**: Move early quit history authorization to StorageReadBeforeHook

### Requirements
- [ ] Enforcers and auditors can view early quit history for players in their guild
- [ ] Move from RPC authorization to StorageReadBeforeHook
- [ ] Implement proper storage permission model
- [ ] Maintain existing user-or-operator access pattern

## Enhancement Ticket: Matchmaker State Guild Filtering

**Goal**: Filter matchmaker state data by player's guild membership

### Requirements
- [ ] `matchmaker/state` should only show data for guilds the player is a member of
- [ ] Implement filtering logic in RPC implementation
- [ ] Optimize query performance for guild membership checks

## Enhancement Ticket: Match/Public ListMatch Integration

**Goal**: Integrate match/public into ListMatch as the default behavior

### Requirements
- [ ] Keep `match/public` RPC for now (backward compatibility)
- [ ] Integrate functionality into built-in Nakama ListMatch
- [ ] Make it the default behavior
- [ ] Document migration path for clients
