# Manual Acceptance Test Plan for prep-nevr Branch

## Document Information
- **Document Version**: 1.0
- **Branch**: prep-nevr
- **Target Release**: Nakama v3.30.0-evr
- **Created**: 2025-09-02
- **Last Updated**: 2025-09-02

## Table of Contents
1. [Objectives and Scope](#objectives-and-scope)
2. [Test Environment Setup](#test-environment-setup)
3. [Test Data Requirements](#test-data-requirements)
4. [Acceptance Test Cases](#acceptance-test-cases)
5. [Acceptance Criteria](#acceptance-criteria)
6. [Responsibilities and Timeline](#responsibilities-and-timeline)
7. [Reporting and Documentation](#reporting-and-documentation)
8. [Risk Assessment](#risk-assessment)

## Objectives and Scope

### Primary Objectives
The manual acceptance testing for the prep-nevr branch aims to validate:

1. **Feature Completeness**: All new features and enhancements work as designed
2. **Integration Stability**: All system components work together correctly
3. **Backward Compatibility**: Existing functionality remains unaffected
4. **Performance Acceptability**: System performance meets or exceeds baseline requirements
5. **Security Compliance**: All security requirements are maintained

### Scope of Testing
This acceptance test plan covers **manual, human-driven validation** of:

#### Core Features (from prep-nevr branch analysis):
- ✅ **Discord Integration**
  - Discord Linked Roles functionality
  - Discord bot authentication and commands
  - User linking and role management
- ✅ **Match Data Journal System**
  - EventDispatcher integration
  - MongoDB persistence
  - Redis queue processing
- ✅ **Backend Module Refactoring**
  - Match presence handling
  - Service separation and modularity
- ✅ **Infrastructure Enhancements**
  - Redis and MongoDB integration
  - Docker Compose service orchestration
  - Database migration and setup
- ✅ **EVR Game Server Features**
  - Echo VR matchmaking
  - Game server registration
  - Lobby management
- ✅ **Link Tickets Functionality**
  - User account linking
  - Ticket-based authentication

#### Out of Scope:
- Automated unit/integration test execution
- Performance load testing (manual stress testing only)
- Security penetration testing
- Third-party service dependencies (Discord API rate limits, etc.)

## Test Environment Setup

### Prerequisites
Before beginning acceptance testing, ensure the following infrastructure is available:

#### Required Software Components:
- **Docker & Docker Compose** (latest stable)
- **PostgreSQL 16.8+** (via Docker)
- **Redis** (latest via Docker)  
- **MongoDB** (latest via Docker)
- **Git** (for branch management)
- **curl** (for API testing)
- **Web Browser** (Chrome/Firefox latest for console testing)

#### Discord Test Environment:
- **Discord Bot Application** with proper permissions
- **Discord Test Server** with channels for testing
- **Test Discord Accounts** (minimum 3 accounts for role testing)
- **Discord Bot Token** and **Client Credentials**

#### Network Configuration:
- **Ports Available**: 5432 (PostgreSQL), 6379 (Redis), 27017 (MongoDB)
- **Nakama Ports**: 7349 (Socket), 7350 (API), 7351 (Console)
- **External Access**: Discord OAuth redirects configured

### Environment Setup Steps

#### 1. Infrastructure Preparation
```bash
# Clone and checkout prep-nevr branch
git clone https://github.com/EchoTools/nakama.git
cd nakama
git checkout prep-nevr

# Setup vendor dependencies  
go mod vendor

# Create environment configuration
cp .env.example .env
# Edit .env with Discord credentials and database settings
```

#### 2. Database Services Setup
```bash
# Start required services
docker compose up -d postgres redis mongo

# Wait for services to be ready (30 seconds)
sleep 30

# Verify service health
docker compose ps
```

#### 3. Build and Start Nakama
```bash
# Build Nakama server
make nakama

# Run database migrations
./nakama-debug migrate up --database.address postgres:localdb@127.0.0.1:5432/nakama

# Start Nakama server
./nakama-debug --database.address postgres:localdb@127.0.0.1:5432/nakama --logger.level INFO
```

#### 4. Environment Verification
```bash
# Verify Nakama API is responsive
curl -i http://127.0.0.1:7350/

# Verify Console access
curl -i http://127.0.0.1:7351/

# Verify database connectivity
curl -i http://127.0.0.1:7350/v2/healthcheck
```

## Test Data Requirements

### User Test Data
| User Type | Count | Purpose | Requirements |
|-----------|-------|---------|--------------|
| **Admin Users** | 2 | System administration testing | Console access, full permissions |
| **Standard Users** | 5 | Regular gameplay testing | Discord linked, various experience levels |
| **VR Users** | 3 | Echo VR specific testing | Headset linked, active play history |
| **Bot Test Accounts** | 2 | Discord bot interaction testing | Discord accounts in test server |

### Match and Lobby Test Data
| Data Type | Description | Requirements |
|-----------|-------------|--------------|
| **Test Matches** | Sample match configurations | Various game modes, team sizes |
| **Lobby Sessions** | Pre-configured lobbies | Public, private, ranked configurations |
| **Match History** | Historical match data | For testing data persistence and retrieval |

### Discord Test Environment Data
| Component | Description | Setup Requirements |
|-----------|-------------|-------------------|
| **Test Discord Server** | Private server for testing | Bot permissions, test channels |
| **Role Configuration** | Various role levels | Different permission levels |
| **Channel Setup** | Test channels | Bot interaction, logging channels |

## Acceptance Test Cases

### TC01: System Startup and Health Verification
**Objective**: Verify system components start correctly and health checks pass

#### Prerequisites:
- Clean environment (no running Nakama instances)
- Docker services stopped

#### Test Steps:
1. **Start Infrastructure Services**
   ```bash
   docker compose up -d postgres redis mongo
   ```
   - **Expected**: All services start with healthy status
   - **Verify**: `docker compose ps` shows all services as "healthy"

2. **Build Nakama Server**
   ```bash
   go mod vendor && make nakama
   ```
   - **Expected**: Build completes without errors
   - **Verify**: `nakama-debug` binary is created

3. **Run Database Migrations**
   ```bash
   ./nakama-debug migrate up --database.address postgres:localdb@127.0.0.1:5432/nakama
   ```
   - **Expected**: Migrations complete successfully
   - **Verify**: "Successfully applied migration" messages in output

4. **Start Nakama Server**
   ```bash
   ./nakama-debug --database.address postgres:localdb@127.0.0.1:5432/nakama --logger.level INFO
   ```
   - **Expected**: Server starts without errors
   - **Verify**: "Startup done" message appears
   - **Verify**: Ports 7349, 7350, 7351 are listening

5. **Health Check Verification**
   ```bash
   curl -i http://127.0.0.1:7350/
   curl -i http://127.0.0.1:7350/v2/healthcheck
   ```
   - **Expected**: HTTP 200 responses
   - **Verify**: JSON responses with server information

**Acceptance Criteria**: ✅ All services start successfully, health checks return 200 OK

---

### TC02: Discord Integration - Linked Roles
**Objective**: Verify Discord Linked Roles functionality works correctly

#### Prerequisites:
- Discord bot configured with proper permissions
- Test Discord server with bot added
- Test user accounts in Discord server

#### Test Steps:
1. **Discord Bot Registration Verification**
   ```bash
   # Check Discord endpoint registration
   curl -X OPTIONS http://localhost:7350/discord/linked-roles/metadata -v
   ```
   - **Expected**: 200 OK with CORS headers
   - **Verify**: Proper CORS preflight response

2. **Authentication Requirement Test**
   ```bash
   curl -X GET http://localhost:7350/discord/linked-roles/metadata -v
   ```
   - **Expected**: 401 Unauthorized
   - **Verify**: "Missing Authorization header" message

3. **Discord OAuth Flow Testing**
   - Navigate to Discord OAuth authorization URL
   - Complete authorization flow
   - Exchange authorization code for access token
   - **Expected**: Valid access token received
   - **Verify**: Token has correct scopes (identify, role_connections.write)

4. **Linked Roles Metadata Retrieval**
   ```bash
   curl -X GET http://localhost:7350/discord/linked-roles/metadata \
     -H "Authorization: Bearer [ACCESS_TOKEN]" \
     -H "Content-Type: application/json"
   ```
   - **Expected**: 200 OK with user metadata
   - **Verify**: JSON response contains role connection data

5. **User Data Testing - Headset Linking**
   - Access Nakama console at http://localhost:7351
   - Create test user with linked headset through EVR system
   - Verify `has_headset` returns true for linked users
   - **Expected**: Headset linking data persists correctly

6. **User Data Testing - Play History**
   - Ensure test users have login history in system
   - Verify `has_played_echo` returns true for users with login history
   - **Expected**: Play history data reflects correctly in Discord roles

**Acceptance Criteria**: ✅ Discord OAuth works, role metadata retrieved, user data correctly mapped

---

### TC03: Match Data Journal and EventDispatcher
**Objective**: Verify Match Data Journal integration with EventDispatcher and MongoDB persistence

#### Prerequisites:
- MongoDB service running and accessible
- Redis service running and accessible
- Test match data available

#### Test Steps:
1. **EventDispatcher Initialization**
   - Start Nakama with MongoDB and Redis clients
   - Verify EventDispatcher initializes with match data handling
   - **Expected**: No initialization errors in logs
   - **Verify**: "EventDispatcher initialized" log message

2. **Match Data Event Processing**
   - Create a test match through API
   - Generate match events (player join, game state changes)
   - **Expected**: Events are captured and queued
   - **Verify**: Redis queue contains match data events

3. **MongoDB Persistence Verification**
   - Connect to MongoDB instance
   - Check `nevr.match_data` collection for test data
   - **Expected**: Match data documents are created
   - **Verify**: Document structure matches expected format:
     ```json
     {
       "match_id": "string",
       "events": [
         {
           "created_at": "timestamp",
           "data": "any"
         }
       ],
       "created_at": "timestamp",
       "updated_at": "timestamp"
     }
     ```

4. **Batch Processing Test**
   - Generate multiple match events rapidly
   - Observe Redis queue processing
   - **Expected**: Events are processed in batches of 10
   - **Verify**: MongoDB documents are updated efficiently

5. **Index Performance Test**
   - Query match data by match_id
   - Verify query performance is acceptable (< 100ms)
   - **Expected**: O(log n) performance with btree index
   - **Verify**: Query execution time logs

6. **TTL Cleanup Verification**
   - Verify TTL index exists on `created_at` field (30 days)
   - **Expected**: Automatic cleanup prevents unbounded growth
   - **Verify**: MongoDB TTL index is active

**Acceptance Criteria**: ✅ Match data flows through EventDispatcher to MongoDB, proper indexing, TTL cleanup works

---

### TC04: Backend Module Refactoring and Match Presence
**Objective**: Verify refactored backend modules and match presence handling work correctly

#### Prerequisites:
- Multiple test users available
- Various match configurations ready

#### Test Steps:
1. **Match Join Attempt Testing**
   - Create test match with specific parameters
   - Attempt to join with valid user
   - **Expected**: Join succeeds with proper validation
   - **Verify**: Match presence is updated correctly

2. **Duplicate EvrID Prevention**
   - Attempt to join match with duplicate EvrID
   - **Expected**: Join rejected with "Duplicate EVR-ID join attempt" error
   - **Verify**: Error logged with proper user identification

3. **Lobby Full Scenario**
   - Fill match to capacity
   - Attempt additional join
   - **Expected**: Rejected with "LobbyFull" error
   - **Verify**: Open slots calculation is accurate

4. **Feature Mismatch Testing**
   - Join with user missing required features
   - **Expected**: Rejected with "FeatureMismatch" error
   - **Verify**: Feature requirements properly validated

5. **Team Alignment Testing**
   - Test user assignment to specific teams
   - Verify spectator and moderator role handling
   - **Expected**: Team alignments persist and function correctly
   - **Verify**: Role restrictions work as intended

6. **Match Presence Synchronization**
   - Join/leave multiple users from match
   - Verify presence map consistency
   - **Expected**: Presence map accurately reflects current state
   - **Verify**: Cache rebuilds properly on changes

**Acceptance Criteria**: ✅ Match joining works with proper validation, presence handling is accurate and efficient

---

### TC05: EVR Game Server Registration and Matchmaking
**Objective**: Verify Echo VR specific game server features work correctly

#### Prerequisites:
- Test game server instances available
- Various lobby configurations prepared

#### Test Steps:
1. **Game Server Registration**
   - Register test game server with system
   - **Expected**: Server appears in registry
   - **Verify**: Server URL parameters are correctly parsed

2. **Lobby Creation and Management**
   - Create various lobby types (public, private, ranked)
   - **Expected**: Lobbies are created with correct configurations
   - **Verify**: Lobby parameters match specifications

3. **Matchmaking Process**
   - Submit matchmaking requests with different criteria
   - **Expected**: Suitable matches are found and created
   - **Verify**: Matchmaking algorithms work correctly

4. **Backfill Functionality**
   - Test lobby backfill with partial lobbies
   - **Expected**: Players are added to existing lobbies when appropriate
   - **Verify**: Backfill logic respects lobby settings

5. **Party Follow Feature**
   - Test party leader lobby joining
   - Follow leader with party members
   - **Expected**: Party members successfully follow leader
   - **Verify**: Party integrity maintained during transitions

6. **Server Failsafe Testing**
   - Test lobby creation when no servers available
   - **Expected**: Failsafe mechanisms activate
   - **Verify**: Graceful handling of server shortage

**Acceptance Criteria**: ✅ Game server registration works, matchmaking functions correctly, party features operational

---

### TC06: Link Tickets Functionality
**Objective**: Verify new link tickets functionality for user account linking

#### Prerequisites:
- Test user accounts available
- Various linking scenarios prepared

#### Test Steps:
1. **Link Ticket Generation**
   - Generate link tickets for test users
   - **Expected**: Tickets are created with proper format
   - **Verify**: Ticket data is stored correctly

2. **Ticket Validation**
   - Validate generated tickets with correct data
   - **Expected**: Validation succeeds for valid tickets
   - **Verify**: Ticket expiration is enforced

3. **Account Linking Process**
   - Use valid tickets to link accounts
   - **Expected**: Accounts are successfully linked
   - **Verify**: Link relationships are persistent

4. **Invalid Ticket Handling**
   - Test with expired or malformed tickets
   - **Expected**: Proper error messages for invalid tickets
   - **Verify**: Security measures prevent ticket abuse

5. **Link Ticket Cleanup**
   - Verify used tickets are properly cleaned up
   - **Expected**: Used tickets are removed or marked invalid
   - **Verify**: No orphaned ticket data remains

**Acceptance Criteria**: ✅ Link tickets generate/validate correctly, account linking works, cleanup functions properly

---

### TC07: Console and API Integration
**Objective**: Verify web console and API functionality with new features

#### Prerequisites:
- Web browser available
- Admin user credentials

#### Test Steps:
1. **Console Access**
   - Navigate to http://localhost:7351
   - Login with admin credentials
   - **Expected**: Console loads without errors
   - **Verify**: All dashboard elements are functional

2. **User Management**
   - Create, modify, and delete test users through console
   - **Expected**: User operations complete successfully
   - **Verify**: Changes persist in database

3. **Match Data Viewing**
   - View match data through console interface
   - **Expected**: Match data displays correctly
   - **Verify**: MongoDB integration is visible in console

4. **API Endpoint Testing**
   - Test core API endpoints with curl
   - **Expected**: All endpoints respond correctly
   - **Verify**: Response formats match specifications

5. **Discord Integration Console**
   - View Discord-related data in console
   - **Expected**: Discord user links are visible
   - **Verify**: Role assignments display correctly

**Acceptance Criteria**: ✅ Console functions properly, API endpoints work, new features are accessible through UI

---

### TC08: End-to-End User Journey
**Objective**: Complete end-to-end user journey covering all major features

#### Prerequisites:
- Full system setup complete
- Test Discord server configured
- Multiple test accounts available

#### Test Steps:
1. **New User Registration**
   - Register new user through Discord authentication
   - **Expected**: User account created successfully
   - **Verify**: User appears in Nakama database

2. **Discord Role Assignment**
   - Complete Discord OAuth flow
   - Verify role assignment based on Nakama data
   - **Expected**: Discord roles reflect Nakama user state
   - **Verify**: Role updates occur in real-time

3. **Match Participation**
   - Join available match or lobby
   - Participate in game session
   - **Expected**: Match join succeeds, gameplay data recorded
   - **Verify**: Match data appears in MongoDB

4. **Party Formation and Gameplay**
   - Form party with other test users
   - Join match as party
   - **Expected**: Party stays together through match process
   - **Verify**: Party functionality maintains integrity

5. **Data Persistence Verification**
   - Restart Nakama server
   - Verify all user data and match history persists
   - **Expected**: No data loss during restart
   - **Verify**: MongoDB and PostgreSQL data intact

**Acceptance Criteria**: ✅ Complete user journey functions smoothly, data persists correctly, all integrations work together

## Acceptance Criteria

### Functional Requirements
- ✅ **Discord Integration**: OAuth flow completes successfully, roles sync correctly
- ✅ **Match Data Persistence**: Match events are captured and stored in MongoDB
- ✅ **User Account Management**: Account creation, linking, and management functions work
- ✅ **Game Server Integration**: Echo VR servers register and accept connections
- ✅ **Matchmaking**: Players can find and join appropriate matches
- ✅ **Console Interface**: Administrative functions are accessible and functional

### Performance Requirements
- ✅ **System Startup**: Server starts within 30 seconds
- ✅ **API Response Times**: API calls respond within 500ms under normal load
- ✅ **Match Join Times**: Match joining completes within 5 seconds
- ✅ **Database Queries**: Core queries execute within 100ms

### Quality Requirements
- ✅ **Error Handling**: System gracefully handles error conditions
- ✅ **Data Integrity**: No data corruption during normal operations
- ✅ **Logging**: Appropriate logging for debugging and monitoring
- ✅ **Security**: Unauthorized access attempts are properly rejected

### Integration Requirements
- ✅ **Discord API**: Discord integration functions without rate limit issues
- ✅ **Database Services**: PostgreSQL, MongoDB, and Redis integrate correctly
- ✅ **Docker Services**: All services start and stop properly via Docker Compose
- ✅ **Backward Compatibility**: Existing functionality remains unaffected

## Responsibilities and Timeline

### Team Responsibilities

| Role | Responsibilities | Contact |
|------|------------------|---------|
| **Test Lead** | Overall test coordination, reporting | TBD |
| **QA Engineer** | Test execution, defect reporting | TBD |
| **DevOps Engineer** | Environment setup, infrastructure | TBD |
| **Backend Developer** | Technical guidance, defect fixes | TBD |
| **Discord Admin** | Discord environment management | TBD |

### Timeline (Estimated)

| Phase | Duration | Activities |
|-------|----------|------------|
| **Setup Phase** | 1 day | Environment preparation, test data creation |
| **Core Testing** | 3 days | Execute TC01-TC06 test cases |
| **Integration Testing** | 2 days | Execute TC07-TC08, end-to-end scenarios |
| **Issue Resolution** | 2 days | Address found issues, re-test |
| **Final Validation** | 1 day | Complete acceptance verification |
| **Documentation** | 1 day | Finalize test reports and sign-off |

**Total Estimated Duration**: 10 business days

### Milestones
- **Day 1**: Environment setup complete
- **Day 4**: Core functionality validated
- **Day 6**: Integration testing complete
- **Day 8**: All critical issues resolved
- **Day 10**: Acceptance testing complete

## Reporting and Documentation

### Test Execution Reports
Test results will be documented using the following format:

#### Test Case Report Template
```
Test Case ID: TC0X
Test Case Name: [Name]
Execution Date: [Date]
Tester: [Name]
Status: PASS/FAIL/BLOCKED
Environment: prep-nevr branch

Test Steps:
[Step-by-step execution details]

Results:
[Actual results vs expected results]

Defects Found:
[List of any issues discovered]

Screenshots/Logs:
[Attach relevant evidence]
```

### Defect Reporting
Defects will be reported using GitHub Issues with the following template:

```
Title: [Brief description]
Labels: bug, prep-nevr, acceptance-testing
Priority: High/Medium/Low
Severity: Critical/Major/Minor

Environment:
- Branch: prep-nevr
- OS: [Operating System]
- Browser: [If applicable]

Steps to Reproduce:
1. [Step 1]
2. [Step 2]
3. [Step 3]

Expected Result:
[What should happen]

Actual Result:
[What actually happened]

Additional Information:
[Logs, screenshots, environment details]
```

### Final Acceptance Report
The final acceptance report will include:

1. **Executive Summary**
   - Overall test results
   - Key findings and recommendations
   - Go/No-Go decision

2. **Test Coverage Summary**
   - Test cases executed
   - Pass/Fail statistics
   - Coverage metrics

3. **Defect Summary**
   - Total defects found
   - Defects by severity
   - Resolution status

4. **Environment Details**
   - Test environment configuration
   - Dependencies and versions
   - Any environment-specific limitations

5. **Recommendations**
   - Approval for production deployment
   - Required fixes before release
   - Future improvements

### Communication Plan
- **Daily Standups**: Progress updates during testing phase
- **Weekly Reports**: Summary reports to stakeholders
- **Issue Escalation**: Critical issues reported immediately
- **Final Sign-off**: Formal acceptance documentation

## Risk Assessment

### High Risk Items
| Risk | Impact | Mitigation |
|------|--------|------------|
| **Discord API Rate Limits** | Could block OAuth testing | Use test Discord server, monitor rate limits |
| **Database Service Failures** | Block most testing scenarios | Have backup database instances ready |
| **Environment Setup Complexity** | Delay testing start | Pre-validate setup procedures |
| **Test Data Dependencies** | Block specific test scenarios | Prepare multiple data sets |

### Medium Risk Items
| Risk | Impact | Mitigation |
|------|--------|------------|
| **Browser Compatibility** | Console testing issues | Test with multiple browsers |
| **Network Connectivity** | API testing delays | Have backup network options |
| **Test User Account Limits** | Limit concurrent testing | Coordinate test execution |

### Low Risk Items
| Risk | Impact | Mitigation |
|------|--------|------------|
| **Documentation Gaps** | Minor testing delays | Reference existing documentation |
| **Version Discrepancies** | Minor compatibility issues | Use specified versions |

### Contingency Plans
- **Critical Defect Found**: Immediately escalate to development team, halt testing until resolution
- **Environment Failure**: Switch to backup environment, document impact on timeline
- **Resource Unavailability**: Adjust timeline, reallocate responsibilities
- **External Service Issues**: Use mock services where possible, document limitations

---

## Appendices

### Appendix A: Environment Variables Template
```bash
# Discord Configuration
DISCORD_BOT_TOKEN=your_bot_token_here
DISCORD_CLIENT_ID=your_client_id_here
DISCORD_CLIENT_SECRET=your_client_secret_here

# Database Configuration  
DATABASE_URL=postgres://postgres:localdb@127.0.0.1:5432/nakama
REDIS_URL=redis://127.0.0.1:6379
MONGO_URL=mongodb://127.0.0.1:27017

# Nakama Configuration
NAKAMA_NAME=nakama1
NAKAMA_LOGGER_LEVEL=INFO
```

### Appendix B: Test User Account Template
```json
{
  "test_users": [
    {
      "username": "test_admin_01",
      "email": "admin1@test.local",
      "role": "admin",
      "discord_id": "12345678901234567",
      "evr_headset": true
    },
    {
      "username": "test_user_01", 
      "email": "user1@test.local",
      "role": "user",
      "discord_id": "12345678901234568",
      "evr_headset": true
    }
  ]
}
```

### Appendix C: Useful Commands Reference
```bash
# Docker service management
docker compose up -d postgres redis mongo
docker compose ps
docker compose logs [service_name]

# Nakama server management
make nakama
./nakama-debug migrate up --database.address postgres:localdb@127.0.0.1:5432/nakama
./nakama-debug --database.address postgres:localdb@127.0.0.1:5432/nakama --logger.level DEBUG

# API testing
curl -i http://127.0.0.1:7350/v2/healthcheck
curl "127.0.0.1:7350/v2/account/authenticate/device?create=true" \
  --user "defaultkey:" \
  --data '{"id": "test-device-123"}'

# Database inspection
mongo --host 127.0.0.1:27017
use nevr
db.match_data.find().limit(5)

psql -h 127.0.0.1 -U postgres -d nakama
\dt
SELECT * FROM users LIMIT 5;
```

---

**Document Control**
- **Version**: 1.0
- **Approved By**: [To be filled]
- **Date Approved**: [To be filled]
- **Next Review Date**: [To be filled]

*This document serves as the comprehensive manual acceptance test plan for the prep-nevr branch and should be updated as requirements or implementation details change.*