package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
)

func rpcAuditActor(ctx context.Context, nk runtime.NakamaModule, callerUserID string) string {
	if callerUserID == "" {
		return "unknown"
	}

	if account, err := nk.AccountGetId(ctx, callerUserID); err == nil && account != nil && account.GetCustomId() != "" {
		discordID := account.GetCustomId()
		if isNumericID(discordID) {
			return fmt.Sprintf("<@%s>", discordID)
		}
		return discordID
	}

	return fmt.Sprintf("`%s`", callerUserID)
}

func isNumericID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func sendRPCAuditMessage(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, rpcID, groupID, callerUserID, details string) {
	if rpcID == "" {
		return
	}

	appBot := globalAppBot.Load()
	if appBot == nil || appBot.dg == nil {
		return
	}

	actor := rpcAuditActor(ctx, nk, callerUserID)
	content := fmt.Sprintf("%s invoked `%s`", actor, rpcID)
	if strings.TrimSpace(details) != "" {
		content += ": " + details
	}

	if groupID != "" {
		if gg, err := GuildGroupLoad(ctx, nk, groupID); err == nil && gg != nil {
			if _, err := AuditLogSendGuild(appBot.dg, gg, content); err != nil {
				logger.Warn("Failed to send guild RPC audit log for %s: %v", rpcID, err)
			}
			return
		}
	}

	if err := AuditLogSend(appBot.dg, ServiceSettings().ServiceAuditChannelID, content); err != nil {
		logger.Warn("Failed to send service RPC audit log for %s: %v", rpcID, err)
	}
}
