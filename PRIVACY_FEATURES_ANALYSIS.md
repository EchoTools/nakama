# Privacy Policy Feature Enumeration - EVR Server Analysis

**Analysis Date**: 2025-11-20  
**Scope**: All `server/evr_*.go` files (162 files, ~46,000 lines)  
**Purpose**: Technical identification of privacy-impacting features for legal/privacy review

---

## 1. AUTHENTICATION & ACCOUNT MANAGEMENT

### 1.1 User Authentication
**Privacy Relevance**: Processes persistent identifiers that can track individual users across sessions and devices.

- Platform-specific authentication (Quest, PCVR via Oculus) using device-specific application IDs
- Custom authentication via Discord OAuth integration  
- Link code generation and exchange for account linking
- Device ID authentication and storage
- JWT token generation and validation for session management
- Multiple authentication methods per account (device IDs, Discord, email)

### 1.2 Account Linking & Identity Management
**Privacy Relevance**: Creates associations between external platform identities and internal user profiles, enabling cross-platform tracking.

- Discord account linking with OAuth access token storage
- Platform ID (XPID) to user account mapping
- HMD (headset) serial number association with accounts
- Cross-device account consolidation
- Link ticket system storing client IP, XPID, and login profile data temporarily

### 1.3 Display Name Management  
**Privacy Relevance**: Stores user-chosen identifiers and their historical changes, including ownership tracking.

- Display name history tracking with timestamps per group
- Display name ownership verification and conflict detection
- Group-specific display name customization
- Anonymization features for display names
- Display name change auditing across guild groups

---

## 2. NETWORK & GEOLOCATION TRACKING

### 2.1 IP Address Collection
**Privacy Relevance**: Collects and stores IP addresses which are considered personal data and can reveal approximate location.

- Client IP address logging on every login attempt
- IP address authorization tracking (authorized vs pending)
- IP address history maintenance with timestamps
- IP-based alternate account detection
- Denied IP address lists for access control
- IP geolocation lookups via third-party providers

### 2.2 Geolocation Services
**Privacy Relevance**: Enriches IP data with precise geographic information and behavioral analytics from third parties.

- Integration with IPQS (IP Quality Score) for fraud detection including VPN/proxy detection, fraud scores (0-100), and ISP/organization identification
- Geographic coordinates (latitude/longitude) collection
- City, region, country code determination
- Geohash generation for location-based matching
- ASN (Autonomous System Number) tracking
- Network type identification

### 2.3 Latency & Connection Quality
**Privacy Relevance**: Creates behavioral profiles based on network performance that could correlate with location or access patterns.

- Client-to-server latency measurement and history
- Regional endpoint performance tracking
- Connection quality metrics collection

---

## 3. DEVICE & HARDWARE FINGERPRINTING

### 3.1 Hardware Information Collection
**Privacy Relevance**: Creates unique device fingerprints from hardware specifications that enable persistent tracking even across account changes.

- HMD (headset) serial numbers
- Headset type/model identification
- CPU model and core count (physical and logical)
- GPU/video card identification
- Total system memory
- Dedicated GPU memory
- Network adapter type

### 3.2 System Profile Generation
**Privacy Relevance**: Combines multiple hardware characteristics into unique identifiers for alternate account detection.

- Composite system profiles combining headset, CPU, GPU, memory specifications
- System profile comparison for account relationship detection
- Hardware configuration history tracking

---

## 4. DISCORD INTEGRATION & EXTERNAL LINKING

### 4.1 Discord Account Integration
**Privacy Relevance**: Shares user data with Discord (third party) and stores Discord-specific identifiers persistently.

- Discord user ID storage and mapping to internal user IDs
- Discord OAuth token storage (access and refresh tokens)
- Discord guild (server) membership synchronization
- Discord role assignment based on account status
- Discord username, global name, and avatar synchronization
- Discord 2FA status verification

### 4.2 Guild/Group Management via Discord
**Privacy Relevance**: Creates persistent associations between Discord community membership and gameplay data.

- Discord guild ID to internal group ID mapping
- Automatic guild group creation on bot join
- Member role synchronization with Discord
- Guild ban synchronization
- Guild membership change tracking
- Cross-guild user activity tracking

### 4.3 Discord Bot Communications
**Privacy Relevance**: Sends user-specific messages and notifications to users via Discord DMs potentially containing personal information.

- Direct message notifications to users
- Automated messages about account linking status
- Display name conflict notifications
- In-game activity notifications sent to Discord
- Audit log generation in Discord channels
- Discord outfit/appearance integration

---

## 5. MATCHMAKING & GAMEPLAY TRACKING

### 5.1 Skill Rating & Ranking Systems
**Privacy Relevance**: Creates persistent behavioral profiles based on gameplay performance.

- OpenSkill rating calculation (Mu, Sigma values)
- Rank percentile tracking across game modes
- Rating history per game mode and time period
- Skill rating ordinal calculations
- Matchmaking division assignment
- Performance-based rank predictions

### 5.2 Match History & Participation
**Privacy Relevance**: Creates detailed records of when, where, and with whom users play.

- Match participation records with timestamps
- Team assignments and teammates identification
- Match duration and completion tracking
- Spectator mode participation logging
- Private vs public match differentiation
- Group-specific match participation

### 5.3 Player Statistics Collection
**Privacy Relevance**: Aggregates detailed gameplay metrics that reveal play patterns and time investment.

- Per-mode statistics (Arena, Combat, Social)
- Daily, weekly, and all-time stat tracking
- Games played counters
- Lobby time accumulation
- Game server time tracking
- Level progression data
- Player loudness/voice activity metrics

---

## 6. BEHAVIORAL ANALYSIS & MODERATION

### 6.1 Early Quit Tracking
**Privacy Relevance**: Monitors user behavior to identify patterns that may affect reputation or access.

- Early quit detection and counting
- Session abandonment tracking
- Pattern analysis for habitual quitting
- Early quit statistics per user

### 6.2 Enforcement & Suspension Records
**Privacy Relevance**: Maintains permanent records of moderation actions and behavioral issues.

- Suspension records with expiry dates
- Ban reasons and enforcer identification
- Community values requirement flags
- Enforcement journal storage
- Guild-level suspension inheritance
- Global ban tracking and synchronization
- Auditor notes on user behavior

### 6.3 Alternate Account Detection
**Privacy Relevance**: Uses multiple data points to identify relationships between accounts, potentially revealing attempts at anonymity.

- First-degree alternate detection via IP, hardware, system profile matching
- Second-degree alternate relationship mapping
- Disabled alternate account flagging
- Bidirectional alternate account relationship maintenance
- Group notification system for detected alternates
- Pattern-based account relationship search

---

## 7. SOCIAL FEATURES & RELATIONSHIPS

### 7.1 Friend Relationships
**Privacy Relevance**: Stores social graph data revealing user relationships and social patterns.

- Friend list storage and synchronization
- Friend status tracking (online/offline)
- Friendship history maintenance

### 7.2 Party & Group Management
**Privacy Relevance**: Tracks real-time social groupings and communication patterns.

- Party membership tracking
- Party group state persistence
- Active party group indexing
- Party status synchronization with Discord

### 7.3 Voice & Communication Controls
**Privacy Relevance**: Stores user preferences about who they communicate with, revealing social dynamics.

- Muted players list per user
- Ghosted players list per user
- Communication preference persistence

---

## 8. VRML (VR Master League) INTEGRATION

### 8.1 VRML Account Linking
**Privacy Relevance**: Links accounts to external competitive gaming service, sharing user identity across platforms.

- VRML OAuth integration with authorization code flow
- VRML access token storage
- VRML user profile synchronization
- VRML entitlement verification

### 8.2 VRML Event & Ledger Integration
**Privacy Relevance**: Shares competitive gaming participation data with third party.

- VRML event participation tracking
- VRML ledger entries for competitive matches
- VRML team/roster associations
- VRML season participation records

---

## 9. IN-APP PURCHASES & VIRTUAL GOODS

### 9.1 IAP Processing
**Privacy Relevance**: Processes payment-related information and purchase history.

- In-app purchase receipt validation
- Purchase history tracking
- Platform receipt verification (Quest, PCVR)

### 9.2 Cosmetic & Loadout Tracking
**Privacy Relevance**: Stores user preferences and purchases that may be monetized.

- Cosmetic item unlocks and ownership
- Loadout configurations
- Jersey number preferences
- Cosmetic loadout history
- New unlock tracking

---

## 10. TELEMETRY & LOGGING

### 10.1 Remote Log Collection
**Privacy Relevance**: Collects client-side application logs that may contain debugging information and usage patterns.

- Remote log journal storage per user
- Session-specific log aggregation
- Timestamped log message retention (7-day window)
- Debug mode enablement tracking
- Log message size limiting (4MB per user)

### 10.2 Telemetry Streams
**Privacy Relevance**: Provides real-time gameplay data streams to authorized parties.

- Match telemetry stream access control
- Real-time gameplay event streaming
- Role-based telemetry access permissions

### 10.3 Metrics & Analytics
**Privacy Relevance**: Aggregates user behavior into metrics for service optimization.

- Matchmaking queue metrics
- Server connection metrics
- Performance timing metrics
- API usage metrics

---

## 11. STORAGE & DATA PERSISTENCE

### 11.1 User-Specific Storage
**Privacy Relevance**: Maintains persistent storage of user data across multiple collections.

- Profile metadata storage with versioning
- Login history storage (unlimited retention)
- Display name history storage
- Group profile storage
- Cache storage for various data types
- VRML-specific storage collection

### 11.2 Leaderboard & Statistics
**Privacy Relevance**: Publicly ranks users based on performance, potentially revealing play patterns.

- Per-mode leaderboards with user rankings
- Time-based leaderboard resets (daily, weekly, all-time)
- Leaderboard record metadata including Discord IDs
- Rank position and percentile exposure
- Public stat visibility

---

## 12. SESSION & PRESENCE TRACKING

### 12.1 Session Management
**Privacy Relevance**: Tracks when users are online and their session characteristics.

- Session ID generation and tracking
- Session start/end timestamps
- Active session registry
- Session parameter storage (region, language, client version)
- Session authentication state

### 12.2 Presence & Online Status
**Privacy Relevance**: Reveals real-time online/offline status and current activity.

- Online status tracking
- Match presence tracking
- Broadcaster/spectator presence
- Game server presence indication
- Party presence synchronization

---

## 13. GAME-SPECIFIC DATA

### 13.1 Match State & Game Data
**Privacy Relevance**: Records detailed in-game actions and states during matches.

- Player positions and movements in matches
- Team assignments and switches
- Score and performance data per match
- Goal/objective completion tracking
- Match player update logs

### 13.2 Profile Customization
**Privacy Relevance**: Stores user preferences and settings that may reveal personal characteristics.

- Legal consent acceptance records
- Game pause settings
- Combat loadout preferences (weapon, grenade, ability, dominant hand)
- Customization preferences
- Language/locale preferences
- Matchmaking division preferences

---

## 14. BROADCASTER & SPECTATOR FEATURES

### 14.1 Broadcaster Registry
**Privacy Relevance**: Tracks users authorized to broadcast or spectate matches.

- Broadcaster session tracking
- Spectator access permissions
- Match observation logging
- Broadcaster endpoint associations

---

## 15. INTEGRATION WITH EXTERNAL SERVICES

### 15.1 Third-Party Data Providers
**Privacy Relevance**: Shares IP addresses and potentially other data with external services for enrichment.

- IPQS (IP Quality Score) integration for fraud/VPN detection
- VRML API integration for competitive gaming data
- Discord API integration for identity and community features

### 15.2 OAuth & External Authentication
**Privacy Relevance**: Uses third-party authentication services that may process user data.

- Discord OAuth flow
- VRML OAuth flow with PKCE
- Refresh token management for sustained access

---

## 16. ACCOUNT SETTINGS & PREFERENCES

### 16.1 Privacy Control Settings
**Privacy Relevance**: Stores user preferences about privacy and communication.

- Discord message relay preferences
- Discord debug message enablement
- Remote log enablement flags
- Display name override controls

### 16.2 Global Settings
**Privacy Relevance**: Server-wide configurations that may affect privacy practices.

- Service guild ID configuration
- Pruning settings for inactive accounts
- Global operator access configurations

---

## 17. DATA RETENTION & CLEANUP

### 17.1 Automatic Pruning
**Privacy Relevance**: Implements data retention policies that affect how long personal data is kept.

- Guild group pruning for orphaned groups
- Login history size limiting (5MB per user)
- Remote log retention (7-day window)
- Expired session cleanup
- Pending authorization cleanup (10-minute expiry)
- Authorized IP cleanup (30-day inactivity)

### 17.2 Data Migration & Updates
**Privacy Relevance**: Modifies user data structures potentially affecting data portability.

- Account migration routines
- Enforcement journal migrations
- Login history rebuilds
- Leaderboard pruning operations

---

## SUMMARY OF PRIVACY-RELEVANT DATA CATEGORIES

This analysis identifies the following categories of data collected/processed:

1. **Identifiers**: User IDs, Discord IDs, Device IDs, XPID, Session IDs, HMD Serial Numbers
2. **Network Data**: IP Addresses, Geolocation, ISP, ASN, VPN Detection, Fraud Scores
3. **Device Information**: Hardware specifications, system profiles, headset types
4. **Social Data**: Friend relationships, party memberships, communication preferences
5. **Performance Data**: Skill ratings, statistics, match history, playtime
6. **Behavioral Data**: Early quits, enforcement records, alternate account relationships
7. **External Identities**: Discord accounts, VRML accounts
8. **Communication**: Discord messages, in-game voice settings
9. **Preferences**: Display names, cosmetic selections, game settings, legal consents
10. **Logs & Telemetry**: Client logs, server logs, debug information, metrics
11. **Commercial**: IAP receipts, virtual goods ownership
12. **Temporal**: Timestamps for logins, matches, account changes, last seen

---

## NOTES FOR PRIVACY POLICY DEVELOPMENT

- Data collection spans multiple categories of personal information
- Extensive use of third-party services (Discord, IPQS, VRML) for data enrichment
- Persistent tracking across sessions, devices, and accounts
- Automated behavioral analysis for fraud/abuse detection
- Data retention varies by data type (some indefinite, some time-limited)
- Cross-platform identity linkage capabilities
- Public exposure of some data via leaderboards
- Real-time data sharing via telemetry streams to authorized users
- Bi-directional data synchronization with Discord
- Multiple mechanisms for user identification and tracking

