package server

import (
	"errors"
	"testing"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"
)

func TestReconcileOrphanGuilds(t *testing.T) {
	guildA := &discordgo.Guild{ID: "1111", Name: "Guild A"}
	guildB := &discordgo.Guild{ID: "2222", Name: "Guild B"}
	guildC := &discordgo.Guild{ID: "3333", Name: "Guild C"}

	tests := []struct {
		name          string
		orphans       []*discordgo.Guild
		failIDs       map[string]bool
		wantRemaining []string
	}{
		{
			name:          "all syncs succeed leaves no orphan candidates",
			orphans:       []*discordgo.Guild{guildA, guildB},
			failIDs:       map[string]bool{},
			wantRemaining: nil,
		},
		{
			name:          "all syncs fail leaves all orphan candidates",
			orphans:       []*discordgo.Guild{guildA, guildB},
			failIDs:       map[string]bool{"1111": true, "2222": true},
			wantRemaining: []string{"1111", "2222"},
		},
		{
			name:          "only failed syncs remain orphan candidates",
			orphans:       []*discordgo.Guild{guildA, guildB, guildC},
			failIDs:       map[string]bool{"2222": true},
			wantRemaining: []string{"2222"},
		},
		{
			name:          "no orphans means no sync attempts and no candidates",
			orphans:       nil,
			failIDs:       map[string]bool{},
			wantRemaining: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := NewRuntimeGoLogger(zap.NewNop())

			syncCalls := make(map[string]int)
			syncFn := func(guild *discordgo.Guild) error {
				syncCalls[guild.ID]++
				if tt.failIDs[guild.ID] {
					return errors.New("sync failed")
				}
				return nil
			}

			remaining := reconcileOrphanGuilds(logger, tt.orphans, syncFn)

			// Every orphan gets exactly one sync attempt.
			for _, g := range tt.orphans {
				if syncCalls[g.ID] != 1 {
					t.Errorf("guild %s: got %d sync attempts, want 1", g.ID, syncCalls[g.ID])
				}
			}
			if len(syncCalls) != len(tt.orphans) {
				t.Errorf("got sync attempts for %d guilds, want %d", len(syncCalls), len(tt.orphans))
			}

			var remainingIDs []string
			for _, g := range remaining {
				remainingIDs = append(remainingIDs, g.ID)
			}
			if len(remainingIDs) != len(tt.wantRemaining) {
				t.Fatalf("got remaining %v, want %v", remainingIDs, tt.wantRemaining)
			}
			for i, id := range tt.wantRemaining {
				if remainingIDs[i] != id {
					t.Errorf("remaining[%d] = %s, want %s", i, remainingIDs[i], id)
				}
			}
		})
	}
}
