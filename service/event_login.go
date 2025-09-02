package service

import (
	"context"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

func (d *EventDispatcher) handleUserAuthenticated(ctx context.Context, logger runtime.Logger, e *EventUserAuthenticated) error {

	if e.UserID == "" {
		return fmt.Errorf("user ID is empty")
	}
	var (
		err    error
		nk     = d.nk
		dg     = d.dg
		userID = e.UserID
	)

	// Record metrics
	metricsTags := loginMetricsTags(e.LoginPayload)
	d.nk.MetricsCounterAdd("login_success", metricsTags, 1)
	d.nk.MetricsTimerRecord("login_process_latency", metricsTags, e.LoginDuration)

	// Load or create the login history for this user
	loginHistory := NewLoginHistory(userID)
	if err := StorableReadNk(ctx, nk, userID, loginHistory, true); err != nil {
		return fmt.Errorf("failed to load login history: %w", err)
	}

	// Update the last used time for their ip
	isNew, allowed := loginHistory.Update(e.XPID, e.ClientIP, e.LoginPayload, e.IsWebSocketAuthenticated)

	if allowed && isNew {
		if err := SendIPAuthorizationNotification(dg, userID, e.ClientIP); err != nil {
			// Log the error, but don't return it.
			logger.WithField("error", err).Warn("Failed to send IP authorization notification")
		}
	}

	hasDiabledAlts, err := loginHistory.UpdateAlternates(ctx, logger, nk)
	if err != nil {
		return fmt.Errorf("failed to update alternates: %w", err)
	}

	if err := StorableWriteNk(ctx, nk, userID, loginHistory); err != nil {
		return fmt.Errorf("failed to store login history: %w", err)
	}

	if hasDiabledAlts && ServiceSettings().KickPlayersWithDisabledAlternates {
		go func() {

			// Set random time to disable and kick player
			var (
				firstIDs, _        = loginHistory.AlternateIDs()
				altNames           = make([]string, 0, len(loginHistory.AlternateMatches))
				accountMap         = make(map[string]*api.Account, len(loginHistory.AlternateMatches))
				delayMin, delayMax = 1, 4
				kickDelay          = time.Duration(delayMin+rand.Intn(delayMax)) * time.Minute
			)

			if accounts, err := nk.AccountsGetId(ctx, append(firstIDs, userID)); err != nil {
				logger.Error("failed to get alternate accounts: %v", err)
				return
			} else {
				for _, a := range accounts {
					accountMap[a.User.Id] = a
					altNames = append(altNames, fmt.Sprintf("<@%s> (%s)", a.CustomId, a.User.Username))
				}
			}

			if len(altNames) == 0 || accountMap[userID] == nil {
				logger.Error("failed to get alternate accounts: %v", err)
				return
			}

			slices.Sort(altNames)
			altNames = slices.Compact(altNames)

			// Send audit log message
			content := fmt.Sprintf("<@%s> (%s) has disabled alternates, disconnecting session(s) in %d seconds.\n%s", accountMap[userID].CustomId, accountMap[userID].User.Username, int(kickDelay.Seconds()), strings.Join(altNames, ", "))
			AuditLogSend(dg, ServiceSettings().ServiceAuditChannelID, content)

			logger.WithField("delay", kickDelay).Info("kicking (with delay) user %s has disabled alternates", userID)
			<-time.After(kickDelay)
			if c, err := DisconnectUserID(ctx, nk, userID, true, true, false); err != nil {
				logger.Error("failed to disconnect user: %v", err)
			} else {
				logger.Info("user %s disconnected: %v sessions", userID, c)
			}
		}()
	}

	return nil
}

func loginMetricsTags(in *evr.LoginProfile) map[string]string {

	systemInfoTags := map[string]any{
		"is_vr":              in.SystemInfo.HeadsetType == "No VR",
		"is_pcvr":            in.SystemInfo.MemoryTotal == 0,
		"build_version":      in.BuildNumber,
		"cpu_model":          in.SystemInfo.CPUModel,
		"gpu_model":          in.SystemInfo.VideoCard,
		"network_type":       in.SystemInfo.NetworkType,
		"total_memory":       in.SystemInfo.MemoryTotal,
		"num_logical_cores":  in.SystemInfo.NumLogicalCores,
		"num_physical_cores": in.SystemInfo.NumPhysicalCores,
		"driver_version":     in.SystemInfo.DriverVersion,
		"headset_type":       normalizeHeadsetType(in.SystemInfo.HeadsetType),
		"build_number":       in.BuildNumber,
		"app_id":             in.AppId,
		"publisher_lock":     strings.TrimSpace(in.PublisherLock),
	}

	tags := make(map[string]string, len(systemInfoTags))
	for k, v := range systemInfoTags {
		tags[k] = strings.TrimSpace(fmt.Sprintf("%v", v))
	}
	return tags
}

// normalizes all the meta headset types to a common format
var headsetMappings = func() map[string]string {

	mappings := map[string][]string{
		"Meta Quest 1":   {"Quest", "Oculus Quest"},
		"Meta Quest 2":   {"Quest 2", "Oculus Quest2"},
		"Meta Quest Pro": {"Quest Pro"},
		"Meta Quest 3":   {"Quest 3", "Oculus Quest3"},
		"Meta Quest 3S":  {"Quest 3S", "Oculus Quest3S"},

		"Meta Quest 1 (Link)":   {"Quest (Link)", "Oculus Quest (Link)"},
		"Meta Quest 2 (Link)":   {"Quest 2 (Link)", "Oculus Quest2 (Link)"},
		"Meta Quest Pro (Link)": {"Quest Pro (Link)", "Oculus Quest Pro (Link)", "Meta Quest Pro (Link)"},
		"Meta Quest 3 (Link)":   {"Quest 3 (Link)", "Oculus Quest3 (Link)"},
		"Meta Quest 3S (Link)":  {"Quest 3S (Link)", "Oculus Quest3S (Link)"},

		"Meta Rift CV1":    {"Oculus Rift CV1"},
		"Meta Rift S":      {"Oculus Rift S"},
		"HTC Vive Elite":   {"Vive Elite"},
		"HTC Vive MV":      {"Vive MV", "Vive. MV"},
		"Bigscreen Beyond": {"Beyond"},
		"Valve Index":      {"Index"},
		"Potato Potato 4K": {"Potato VR"},
	}

	// Create a reverse mapping
	reverse := make(map[string]string)
	for k, v := range mappings {
		for _, s := range v {
			reverse[s] = k
		}
	}

	return reverse
}()

func normalizeHeadsetType(headset string) string {
	if headset == "" {
		return "Unknown"
	}

	if v, ok := headsetMappings[headset]; ok {
		return v
	}

	return headset
}
