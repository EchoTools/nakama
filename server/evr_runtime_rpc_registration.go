package server

import (
	"context"
	"database/sql"

	"github.com/heroiclabs/nakama-common/runtime"
)

// RegisterEVRRPCs registers all EVR RPC endpoints with authorization middleware
func RegisterEVRRPCs(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer, rpcHandler *RPCHandler, sbmm *SkillBasedMatchmaker) error {
	// Define all RPC registrations with their permissions in one place
	// This declarative approach ensures endpoint IDs are defined only once
	rpcs := []RPCRegistration{
		// Account management
		// account/search - Authenticated users, filtered by shared guild membership in RPC
		{
			ID:      "account/search",
			Handler: AccountSearchRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{}, // Any authenticated user, filtered in RPC
			},
		},
		// account/lookup - Authenticated users who share a guild
		{
			ID:      "account/lookup",
			Handler: rpcHandler.AccountLookupRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{}, // Any authenticated user, validated via shared guild
			},
		},
		{ID: "account/authenticate/password", Handler: AuthenticatePasswordRPC},
		// account/break_alternates - Global operators only
		{
			ID:      "account/break_alternates",
			Handler: BreakAlternatesRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{GroupGlobalOperators},
			},
		},

		// Leaderboards - Any authenticated user can view
		{
			ID:      "leaderboard/haystack",
			Handler: rpcHandler.LeaderboardHaystackRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{}, // Any authenticated user
			},
		},
		{
			ID:      "leaderboard/records",
			Handler: rpcHandler.LeaderboardRecordsListRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{}, // Any authenticated user
			},
		},

		// Device linking
		{ID: "link/device", Handler: LinkDeviceRpc},
		{ID: "link/usernamedevice", Handler: LinkUserIdDeviceRpc},
		{ID: "link", Handler: LinkingAppRpc},
		{ID: "signin/discord", Handler: DiscordSignInRpc},

		// Match management
		{ID: "match/public", Handler: rpcHandler.MatchListPublicRPC},
		{ID: "match", Handler: MatchRPC},
		// match/prepare - Global ops and users with allocator role
		{
			ID:      "match/prepare",
			Handler: PrepareMatchRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{GroupGlobalOperators},
				// TODO: Add role-based check for allocator role
			},
		},
		{ID: "match/allocate", Handler: AllocateMatchRPC},
		// match/terminate - Global ops, guild enforcers, match creator, server owner
		{
			ID:      "match/terminate",
			Handler: shutdownMatchRpc,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{GroupGlobalOperators},
				// TODO: Add multi-role check (guild enforcers, match creator, server owner)
			},
		},
		{ID: "match/build", Handler: BuildMatchRPC},

		// Player management
		// player/setnextmatch - Players can set their own, auditors/enforcers can set others
		{
			ID:      "player/setnextmatch",
			Handler: SetNextMatchRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{GroupGlobalOperators}, // Any authenticated user
				// TODO: Add role-based check for auditors/enforcers
			},
		},
		{ID: "player/statistics", Handler: PlayerStatisticsRPC},
		// player/kick - Global ops, guild enforcers, match creator, server owner
		{
			ID:      "player/kick",
			Handler: KickPlayerRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{GroupGlobalOperators},
				// TODO: Add multi-role check (guild enforcers, match creator, server owner)
			},
		},
		{ID: "player/profile", Handler: UserServerProfileRPC},
		// player/matchlock - Global ops and guild moderators
		{
			ID:      "player/matchlock",
			Handler: MatchLockRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{GroupGlobalOperators},
				// TODO: Add role-based check for guild moderators
			},
		},
		{
			ID:      "player/matchlock/status",
			Handler: GetMatchLockStatusRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{GroupGlobalOperators},
				// TODO: Add role-based check for guild moderators
			},
		},

		// Player outfits - Owner only
		{
			ID:      "player/outfit/save",
			Handler: PlayerOutfitSaveRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{}, // Any authenticated user (owner check in RPC)
				// TODO: Add owner validation in middleware
			},
		},
		{
			ID:      "player/outfit/list",
			Handler: PlayerOutfitListRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{}, // Any authenticated user (owner check in RPC)
				// TODO: Add owner validation in middleware
			},
		},
		{
			ID:      "player/outfit/load",
			Handler: PlayerOutfitLoadRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{}, // Any authenticated user (owner check in RPC)
				// TODO: Add owner validation in middleware
			},
		},
		{
			ID:      "player/outfit/delete",
			Handler: PlayerOutfitDeleteRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{}, // Any authenticated user (owner check in RPC)
				// TODO: Add owner validation in middleware
			},
		},

		// EVR service status
		{ID: "evr/servicestatus", Handler: rpcHandler.ServiceStatusRPC},

		// Matchmaking
		{ID: "matchmaking/settings", Handler: MatchmakingSettingsRPC},
		{ID: "matchmaker/stream", Handler: MatchmakerStreamRPC},
		// matchmaker/state - Filter to guilds player is member of (in RPC)
		{
			ID:      "matchmaker/state",
			Handler: MatchmakerStateRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{}, // Any authenticated user, filtered in RPC
			},
		},
		{ID: "matchmaker/candidates", Handler: MatchmakerCandidatesRPCFactory(sbmm)},
		{ID: "matchmaker/config", Handler: MatchmakerConfigRPC},

		// Stream management - Global operators only
		{ID: "stream/join", Handler: StreamJoinRPC},

		// Server management
		{ID: "server/score", Handler: ServerScoreRPC},
		// server/scores - Any authenticated user
		{
			ID:      "server/scores",
			Handler: ServerScoresRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{}, // Any authenticated user
			},
		},

		{
			ID:      "guildgroup",
			Handler: GuildGroupGetRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{},
			},
		},
		{
			ID:      "guild/group/update",
			Handler: GuildGroupUpdateRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{},
			},
		},
		{
			ID:      "guild/group/roles/update",
			Handler: GuildGroupRolesUpdateRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{},
			},
		},
		{
			ID:      "guild/group/transfer",
			Handler: GuildGroupTransferOwnershipRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{},
			},
		},
		{
			ID:      "guild/group/leave",
			Handler: GuildGroupLeaveRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{},
			},
		},
		{
			ID:      "guild/group/delete",
			Handler: GuildGroupDeleteRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{},
			},
		},
		{
			ID:      "guild/discord/roles",
			Handler: rpcHandler.GuildDiscordRolesRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{},
			},
		},

		// Enforcement - Global Operators or guild enforcers
		{
			ID:      "enforcement/kick",
			Handler: EnforcementKickRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{GroupGlobalOperators},
				// TODO: Add guild enforcer check in middleware
			},
		},
		{
			ID:      "enforcement/journals",
			Handler: EnforcementJournalListRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{GroupGlobalOperators},
				// TODO: Add guild owner check in middleware
			},
		},
		{
			ID:      "enforcement/record/edit",
			Handler: EnforcementRecordEditRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{GroupGlobalOperators},
				// TODO: Add guild enforcer check in middleware
			},
		},

		// Early quit - Users can view their own, operators can view any
		{
			ID:      "earlyquit/history",
			Handler: EarlyQuitHistoryRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{}, // Custom authorization in RPC (user or operator)
				// TODO: Move to middleware with user-or-operator pattern
			},
		},

		// Client role — any authenticated user
		{
			ID:      "get_client_role",
			Handler: GetClientRoleRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{},
			},
		},

		// TOT test upload — Global Testers admin only (enforced inside handler)
		{
			ID:      "tot/tests/upload",
			Handler: TotTestsUploadRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{GroupGlobalTesters},
			},
		},
		// TOT test list — any Global Tester
		{
			ID:      "tot/tests/list",
			Handler: TotTestsListRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{GroupGlobalTesters},
			},
		},
		// TOT test create/update/delete — Global Tester admin only (enforced inside handler)
		{
			ID:      "tot/tests/create",
			Handler: TotTestsCreateRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{GroupGlobalTesters},
			},
		},
		{
			ID:      "tot/tests/update",
			Handler: TotTestsUpdateRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{GroupGlobalTesters},
			},
		},
		{
			ID:      "tot/tests/delete",
			Handler: TotTestsDeleteRPC,
			Permission: &RPCPermission{
				RequireAuth:   true,
				AllowedGroups: []string{GroupGlobalTesters},
			},
		},

		// Legacy/misc
		{ID: "importloadouts", Handler: ImportLoadoutsRpc},
		{ID: "forcecheck", Handler: CheckForceUserRPC},
	}

	// Register RPCs with authorization middleware
	for _, rpc := range rpcs {
		var perm RPCPermission
		if rpc.Permission != nil {
			perm = *rpc.Permission
		} else {
			perm = DefaultRPCPermission() // Default: Auth + Global Operators
		}

		wrappedRPC := WithRPCAuthorization(rpc.ID, perm, rpc.Handler)
		if err := initializer.RegisterRpc(rpc.ID, wrappedRPC); err != nil {
			return err
		}
	}

	return nil
}
