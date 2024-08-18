package server

import (
	"context"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
)

type LinkMessage struct {
	MatchID MatchID
	Channel *discordgo.Channel
	Message *discordgo.Message
}
type LinkMeta struct {
	MatchID MatchID
	TrackID TrackID
}

type TrackID struct {
	ChannelID string
	MessageID string
}

type TaxiLinkRegistry struct {
	sync.Mutex
	ctx         context.Context
	ctxCancelFn context.CancelFunc
	nk          runtime.NakamaModule
	logger      runtime.Logger
	dg          *discordgo.Session

	trackQueueCh chan LinkMeta        // Queue for tracking reactions
	clearQueueCh chan ClearQueueEntry // Queue for clearing reactions in groups
	node         string
	tracked      map[TrackID]MatchID
}

type ClearQueueEntry struct {
	TrackID TrackID
	Remove  bool
}

func NewTaxiLinkRegistry(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, config Config, dg *discordgo.Session) *TaxiLinkRegistry {
	ctx, cancel := context.WithCancel(ctx)

	registry := &TaxiLinkRegistry{
		ctx:          ctx,
		node:         config.GetName(),
		ctxCancelFn:  cancel,
		nk:           nk,
		logger:       logger,
		dg:           dg,
		tracked:      make(map[TrackID]MatchID),
		trackQueueCh: make(chan LinkMeta, 100),
		clearQueueCh: make(chan ClearQueueEntry, 100),
	}

	// Process the track queue
	// Clear reactions every 3 seconds
	go func() {
		toClear := make(map[TrackID]bool) // true clears bot reactions too
		clearTicker := time.NewTicker(3 * time.Second)
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-registry.trackQueueCh:

				registry.ProcessLinkMessage(t.MatchID, t.TrackID, false)

			case entry := <-registry.clearQueueCh:

				toClear[entry.TrackID] = entry.Remove

			case <-clearTicker.C:
				for t, remove := range toClear {
					delete(toClear, t)
					if remove {
						defer registry.delete(t)
					}

					reactions, err := dg.MessageReactions(t.ChannelID, t.MessageID, TaxiEmoji, 100, "", "")
					if err != nil {
						registry.logger.Warn("Failed to get reactions", "error", err)
						continue
					}
					for _, r := range reactions {
						if !remove && r.ID == dg.State.User.ID {
							continue
						}
						err = dg.MessageReactionRemove(t.ChannelID, t.MessageID, TaxiEmoji, r.ID)
						if err != nil {
							registry.logger.Warn("Failed to remove reaction", "error", err)
							continue
						}
					}
				}
			}
		}
	}()

	return registry
}

func (e *TaxiLinkRegistry) Stop() {
	e.ctxCancelFn()
}

func (r *TaxiLinkRegistry) load(t TrackID) (MatchID, bool) {
	r.Lock()
	defer r.Unlock()
	m, found := r.tracked[t]
	return m, found
}

func (r *TaxiLinkRegistry) store(t TrackID, m MatchID) {
	r.Lock()
	defer r.Unlock()
	r.tracked[t] = m
}

func (r *TaxiLinkRegistry) delete(t TrackID) {
	r.Lock()
	defer r.Unlock()
	delete(r.tracked, t)
}

func (r *TaxiLinkRegistry) track(channelID, messageID string, matchID MatchID) {
	t := TrackID{
		ChannelID: channelID,
		MessageID: messageID,
	}
	r.store(t, matchID)
}

func (r *TaxiLinkRegistry) Queue(matchID MatchID, channelID, messageID string) {
	r.trackQueueCh <- LinkMeta{
		MatchID: matchID,
		TrackID: TrackID{
			ChannelID: channelID,
			MessageID: messageID,
		},
	}
}

// Track adds a message to the tracker.
func (r *TaxiLinkRegistry) ProcessLinkMessage(matchID MatchID, t TrackID, isDirectMessage bool) error {

	// Add the taxi reaction
	err := r.dg.MessageReactionAdd(t.ChannelID, t.MessageID, TaxiEmoji)
	if err != nil {
		// Do not track this message
		return err
	}

	r.track(t.ChannelID, t.MessageID, matchID)
	return nil
}

func (e *TaxiLinkRegistry) Remove(t TrackID) {
	e.Clear(t, true)
}

// Clear removes all taxi reactions
func (e *TaxiLinkRegistry) Clear(t TrackID, all bool) {
	e.clearQueueCh <- ClearQueueEntry{
		TrackID: t,
		Remove:  all,
	}
}

// Prune removes all inactive matches, and their reactions
func (e *TaxiLinkRegistry) Prune() (active, pruned int, err error) {
	e.Lock()
	defer e.Unlock()
	// Check all the tracked matches
	for t, m := range e.tracked {

		// Check if the match is still active
		match, err := e.nk.MatchGet(e.ctx, m.String())
		if err == nil && match != nil {
			active++
			continue
		}

		// Remove all the taxi reactions from the channel messages
		e.Clear(t, true)
		pruned++
	}
	return active, pruned, nil
}

// Count returns the number of actively tracked URLs
func (e *TaxiLinkRegistry) Count() (cnt int) {
	e.Lock()
	defer e.Unlock()
	return len(e.tracked)
}
