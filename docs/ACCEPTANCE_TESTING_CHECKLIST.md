# Quick Reference: prep-nevr Acceptance Testing Checklist

This is a condensed checklist for quick validation of the prep-nevr branch features. For detailed test procedures, see [MANUAL_ACCEPTANCE_TEST_PLAN.md](./MANUAL_ACCEPTANCE_TEST_PLAN.md).

## Pre-Testing Setup ✅

### Environment Setup
- [ ] Docker services running (postgres, redis, mongo)
- [ ] Nakama server built and started
- [ ] All ports accessible (5432, 6379, 27017, 7349, 7350, 7351)
- [ ] Discord bot configured with test server

### Health Checks
- [ ] `curl http://127.0.0.1:7350/` returns 200 OK
- [ ] `curl http://127.0.0.1:7350/v2/healthcheck` returns healthy status
- [ ] Console accessible at http://127.0.0.1:7351
- [ ] Database migrations completed successfully

## Core Feature Testing ✅

### Discord Integration
- [ ] Discord OAuth flow completes successfully
- [ ] Role metadata endpoint requires authentication (401 without token)
- [ ] Valid access token returns user metadata (200 OK)
- [ ] Linked users show `has_headset: true` when appropriate
- [ ] Users with play history show `has_played_echo: true`
- [ ] Discord roles sync with Nakama user data

### Match Data Journal & EventDispatcher  
- [ ] Match events are captured and queued in Redis
- [ ] Match data persists to MongoDB `nevr.match_data` collection
- [ ] Document structure includes match_id, events array, timestamps
- [ ] Batch processing handles multiple events efficiently
- [ ] TTL index prevents unbounded data growth (30 days)
- [ ] Query performance is acceptable (< 100ms for match_id lookups)

### Backend Refactoring & Match Presence
- [ ] Valid users can join matches successfully
- [ ] Duplicate EvrID join attempts are rejected
- [ ] Full lobby rejects additional join attempts
- [ ] Feature requirements are validated correctly
- [ ] Team alignments persist and function properly
- [ ] Match presence map stays synchronized

### EVR Game Server Features
- [ ] Game servers can register with the system
- [ ] Various lobby types can be created (public, private, ranked)
- [ ] Matchmaking finds appropriate matches
- [ ] Lobby backfill adds players to existing lobbies
- [ ] Party follow functionality keeps parties together
- [ ] Failsafe mechanisms handle server shortages

### Link Tickets
- [ ] Link tickets can be generated for users
- [ ] Valid tickets successfully link accounts
- [ ] Invalid/expired tickets are properly rejected
- [ ] Used tickets are cleaned up appropriately
- [ ] Account relationships persist correctly

### Console & API Integration
- [ ] Web console loads without errors at :7351
- [ ] User management operations work through console
- [ ] Match data is visible in console interface
- [ ] API endpoints respond correctly to curl tests
- [ ] Discord integration data displays in console

## End-to-End Testing ✅

### Complete User Journey
- [ ] New user registration via Discord authentication
- [ ] Discord role assignment reflects Nakama user state
- [ ] User can join matches and participate in gameplay
- [ ] Party formation and match joining works together
- [ ] All user data persists after server restart

## Performance Validation ✅

### Response Times
- [ ] Server startup completes within 30 seconds
- [ ] API calls respond within 500ms under normal load
- [ ] Match joining completes within 5 seconds
- [ ] Database queries execute within 100ms

### Error Handling
- [ ] Invalid requests return appropriate error messages
- [ ] System gracefully handles connection failures
- [ ] Rate limiting works for Discord API integration
- [ ] Unauthorized access attempts are properly rejected

## Final Verification ✅

### Data Integrity
- [ ] No data corruption during normal operations
- [ ] MongoDB match data is complete and accurate
- [ ] PostgreSQL user data remains consistent
- [ ] Redis queues process without data loss

### Backward Compatibility
- [ ] Existing API endpoints still function
- [ ] Previous user data is accessible
- [ ] No regression in core Nakama features
- [ ] Upgrade path works without data migration issues

## Sign-off Criteria ✅

**All items must be checked before approving prep-nevr for merge to main:**

- [ ] **ALL** checklist items above are verified and working
- [ ] Critical issues have been resolved and re-tested
- [ ] Performance meets or exceeds baseline requirements
- [ ] Documentation is updated for any new procedures
- [ ] Test environment matches expected production environment
- [ ] Final acceptance report has been completed

---

## Quick Command Reference

```bash
# Start testing environment
docker compose up -d postgres redis mongo
make nakama
./nakama-debug migrate up --database.address postgres:localdb@127.0.0.1:5432/nakama
./nakama-debug --database.address postgres:localdb@127.0.0.1:5432/nakama --logger.level INFO

# Quick health checks
curl -i http://127.0.0.1:7350/
curl -i http://127.0.0.1:7350/v2/healthcheck

# Discord endpoints
curl -X OPTIONS http://localhost:7350/discord/linked-roles/metadata -v
curl -X GET http://localhost:7350/discord/linked-roles/metadata -v

# Test authentication
curl "127.0.0.1:7350/v2/account/authenticate/device?create=true" \
  --user "defaultkey:" \
  --data '{"id": "test-device-123"}'
```

## Issues & Notes

**Document any issues found during testing:**

| Issue | Severity | Status | Notes |
|-------|----------|--------|-------|
| | | | |
| | | | |
| | | | |

**Tester**: _________________ **Date**: _____________ **Signature**: _________________

---

*For detailed test procedures and troubleshooting, see the complete [Manual Acceptance Test Plan](./MANUAL_ACCEPTANCE_TEST_PLAN.md).*