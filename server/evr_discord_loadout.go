package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	loadoutAutocompleteEndpoint = "/discord/loadout/autocomplete"
	loadoutAutocompleteTTL      = 15 * time.Second
	loadoutAutocompleteMax      = 25
	maxLoadoutValueLength       = 128
)

var (
	loadoutTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	numericOnlyPattern  = regexp.MustCompile(`^[0-9]+$`)

	loadoutSettableSlots = func() map[string]struct{} {
		slots := make(map[string]struct{})
		for slot := range evr.DefaultCosmeticLoadout().ToMap() {
			slots[slot] = struct{}{}
		}
		return slots
	}()

	globalLoadoutAutocompleteService struct {
		sync.RWMutex
		svc *LoadoutAutocompleteService
	}
)

type loadoutAutocompleteCacheEntry struct {
	expiresAt time.Time
	choices   []*discordgo.ApplicationCommandOptionChoice
}

// LoadoutAutocompleteService serves autocomplete choices for loadout commands and HTTP clients.
type LoadoutAutocompleteService struct {
	logger runtime.Logger
	nk     runtime.NakamaModule

	mu    sync.RWMutex
	cache map[string]loadoutAutocompleteCacheEntry
}

func NewLoadoutAutocompleteService(logger runtime.Logger, nk runtime.NakamaModule) *LoadoutAutocompleteService {
	return &LoadoutAutocompleteService{
		logger: logger,
		nk:     nk,
		cache:  make(map[string]loadoutAutocompleteCacheEntry),
	}
}

func SetGlobalLoadoutAutocompleteService(svc *LoadoutAutocompleteService) {
	globalLoadoutAutocompleteService.Lock()
	defer globalLoadoutAutocompleteService.Unlock()
	globalLoadoutAutocompleteService.svc = svc
}

func GetGlobalLoadoutAutocompleteService() *LoadoutAutocompleteService {
	globalLoadoutAutocompleteService.RLock()
	defer globalLoadoutAutocompleteService.RUnlock()
	return globalLoadoutAutocompleteService.svc
}

func (s *LoadoutAutocompleteService) NameChoices(ctx context.Context, userID, partial string) ([]*discordgo.ApplicationCommandOptionChoice, error) {
	key := strings.Join([]string{"name", userID, strings.ToLower(partial)}, "|")
	if cached := s.getCachedChoices(key); cached != nil {
		return cached, nil
	}

	wardrobe := &Wardrobe{}
	if err := StorableRead(ctx, s.nk, userID, wardrobe, true); err != nil && !isStorageNotFoundError(err) {
		return nil, err
	}

	query := strings.ToLower(strings.TrimSpace(partial))
	names := make([]string, 0, len(wardrobe.Outfits))
	for name := range wardrobe.Outfits {
		if query == "" || strings.Contains(strings.ToLower(name), query) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	choices := makeChoicesFromStrings(names)
	s.setCachedChoices(key, choices)
	return cloneChoices(choices), nil
}

func (s *LoadoutAutocompleteService) SlotChoices(partial string) []*discordgo.ApplicationCommandOptionChoice {
	key := strings.Join([]string{"slot", strings.ToLower(partial)}, "|")
	if cached := s.getCachedChoices(key); cached != nil {
		return cached
	}

	query := strings.ToLower(strings.TrimSpace(partial))
	slots := make([]string, 0, len(loadoutSettableSlots))
	for slot := range loadoutSettableSlots {
		if query == "" || strings.Contains(slot, query) {
			slots = append(slots, slot)
		}
	}
	sort.Strings(slots)
	choices := makeChoicesFromStrings(slots)
	s.setCachedChoices(key, choices)
	return cloneChoices(choices)
}

func (s *LoadoutAutocompleteService) ValueChoices(ctx context.Context, userID, slot, partial string) ([]*discordgo.ApplicationCommandOptionChoice, error) {
	key := strings.Join([]string{"value", userID, strings.ToLower(slot), strings.ToLower(partial)}, "|")
	if cached := s.getCachedChoices(key); cached != nil {
		return cached, nil
	}

	if _, ok := loadoutSettableSlots[slot]; !ok {
		return []*discordgo.ApplicationCommandOptionChoice{}, nil
	}

	unlocked, err := s.UserUnlockedCosmetics(ctx, userID)
	if err != nil {
		return nil, err
	}

	query := strings.ToLower(strings.TrimSpace(partial))
	items := make([]string, 0, len(unlocked))
	for item := range unlocked {
		if query == "" || strings.Contains(strings.ToLower(item), query) {
			items = append(items, item)
		}
	}
	sort.Strings(items)
	choices := makeChoicesFromStrings(items)
	s.setCachedChoices(key, choices)
	return cloneChoices(choices), nil
}

func (s *LoadoutAutocompleteService) UserUnlockedCosmetics(ctx context.Context, userID string) (map[string]bool, error) {
	profile, err := EVRProfileLoad(ctx, s.nk, userID)
	if err != nil {
		return nil, err
	}

	wallet := map[string]int64{}
	if err := json.Unmarshal([]byte(profile.Wallet()), &wallet); err != nil {
		return nil, fmt.Errorf("failed to parse wallet: %w", err)
	}

	unlocks := cosmeticDefaults(profile.EnableAllCosmetics)
	unlocks = walletToCosmetics(wallet, unlocks)

	flattened := make(map[string]bool)
	for _, category := range unlocks {
		for item, unlocked := range category {
			if unlocked {
				flattened[item] = true
			}
		}
	}
	return flattened, nil
}

func (s *LoadoutAutocompleteService) getCachedChoices(key string) []*discordgo.ApplicationCommandOptionChoice {
	s.mu.RLock()
	entry, ok := s.cache[key]
	s.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		if ok {
			s.mu.Lock()
			delete(s.cache, key)
			s.mu.Unlock()
		}
		return nil
	}
	return cloneChoices(entry.choices)
}

func (s *LoadoutAutocompleteService) setCachedChoices(key string, choices []*discordgo.ApplicationCommandOptionChoice) {
	s.mu.Lock()
	s.cache[key] = loadoutAutocompleteCacheEntry{
		expiresAt: time.Now().Add(loadoutAutocompleteTTL),
		choices:   cloneChoices(choices),
	}
	s.mu.Unlock()
}

func cloneChoices(in []*discordgo.ApplicationCommandOptionChoice) []*discordgo.ApplicationCommandOptionChoice {
	out := make([]*discordgo.ApplicationCommandOptionChoice, len(in))
	for idx, choice := range in {
		if choice == nil {
			continue
		}
		copied := *choice
		out[idx] = &copied
	}
	return out
}

func makeChoicesFromStrings(values []string) []*discordgo.ApplicationCommandOptionChoice {
	if len(values) > loadoutAutocompleteMax {
		values = values[:loadoutAutocompleteMax]
	}
	choices := make([]*discordgo.ApplicationCommandOptionChoice, 0, len(values))
	for _, value := range values {
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{Name: value, Value: value})
	}
	return choices
}

func IsLoadoutUsernameAllowed(metadata *GroupMetadata, discordUsername string) bool {
	if metadata == nil {
		return false
	}
	if len(metadata.LoadoutCommandUsernames) == 0 {
		return false
	}
	username := strings.TrimSpace(discordUsername)
	for _, allowed := range metadata.LoadoutCommandUsernames {
		if strings.EqualFold(strings.TrimSpace(allowed), username) {
			return true
		}
	}
	return false
}

func validateLoadoutToken(value string, maxLen int, fieldName string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s cannot be empty", fieldName)
	}
	if len(value) > maxLen {
		return fmt.Errorf("%s is too long", fieldName)
	}
	if !loadoutTokenPattern.MatchString(value) {
		return fmt.Errorf("%s must only contain letters, numbers, or underscores", fieldName)
	}
	if numericOnlyPattern.MatchString(value) {
		return fmt.Errorf("%s cannot be numeric-only", fieldName)
	}
	return nil
}

func applyLoadoutSlot(loadout *evr.CosmeticLoadout, slot, value string) {
	m := loadout.ToMap()
	switch slot {
	case "emote":
		m["emote"] = value
		m["secondemote"] = value
	case "decal":
		m["decal"] = value
		m["decal_body"] = value
	case "pattern":
		m["pattern"] = value
		m["pattern_body"] = value
	case "tint":
		m["tint"] = value
		m["tint_body"] = value
	default:
		m[slot] = value
	}
	loadout.FromMap(m)
}

func isKnownVRMLCosmetic(name string) bool {
	for _, vrml := range AllVRMLCosmetics() {
		if vrml == name {
			return true
		}
	}
	return false
}

func (d *DiscordAppBot) loadoutAutocompleteService() *LoadoutAutocompleteService {
	if svc := GetGlobalLoadoutAutocompleteService(); svc != nil {
		return svc
	}
	svc := NewLoadoutAutocompleteService(d.logger, d.nk)
	SetGlobalLoadoutAutocompleteService(svc)
	return svc
}

func (d *DiscordAppBot) handleLoadoutCommand(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, userID string) error {
	data := i.ApplicationCommandData()
	if len(data.Options) == 0 {
		return editInteractionResponse(s, i, "Missing subcommand.")
	}

	subcommand := data.Options[0]
	options := make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(subcommand.Options))
	for _, option := range subcommand.Options {
		options[option.Name] = option
	}

	wardrobe := &Wardrobe{}
	if err := StorableRead(ctx, d.nk, userID, wardrobe, true); err != nil && !isStorageNotFoundError(err) {
		return fmt.Errorf("failed to load loadouts: %w", err)
	}

	profile, err := EVRProfileLoad(ctx, d.nk, userID)
	if err != nil {
		return fmt.Errorf("failed to load profile: %w", err)
	}

	svc := d.loadoutAutocompleteService()

	switch subcommand.Name {
	case "get":
		nameOpt := options["name"]
		if nameOpt == nil {
			return editInteractionResponse(s, i, "Missing loadout name.")
		}
		name := strings.TrimSpace(nameOpt.StringValue())
		outfit, ok := wardrobe.GetOutfit(name)
		if !ok || outfit == nil {
			return editInteractionResponse(s, i, fmt.Sprintf("Loadout `%s` does not exist.", name))
		}

		profile.LoadoutCosmetics = *outfit
		if err := EVRProfileUpdate(ctx, d.nk, userID, profile); err != nil {
			return fmt.Errorf("failed to apply loadout: %w", err)
		}
		return editInteractionResponse(s, i, fmt.Sprintf("Applied loadout `%s`.", name))

	case "store":
		nameOpt := options["name"]
		if nameOpt == nil {
			return editInteractionResponse(s, i, "Missing loadout name.")
		}
		name := strings.TrimSpace(nameOpt.StringValue())
		if err := validateLoadoutToken(name, MaximumOutfitNameLength, "loadout name"); err != nil {
			return editInteractionResponse(s, i, err.Error())
		}

		_, exists := wardrobe.GetOutfit(name)
		overwrite := false
		if overwriteOpt := options["overwrite"]; overwriteOpt != nil {
			overwrite = overwriteOpt.BoolValue()
		}

		if exists && !overwrite {
			return editInteractionResponse(s, i, "Loadout already exists. Run again with `overwrite:true` to replace it.")
		}
		if !exists && len(wardrobe.Outfits) >= MaximumOutfitCount {
			return editInteractionResponse(s, i, fmt.Sprintf("Cannot store more than %d loadouts.", MaximumOutfitCount))
		}

		wardrobe.SetOutfit(name, profile.LoadoutCosmetics)
		if err := StorableWrite(ctx, d.nk, userID, wardrobe); err != nil {
			return fmt.Errorf("failed to store loadout: %w", err)
		}
		if overwrite {
			return editInteractionResponse(s, i, fmt.Sprintf("Stored loadout `%s` (overwritten).", name))
		}
		return editInteractionResponse(s, i, fmt.Sprintf("Stored loadout `%s`.", name))

	case "set":
		slotOpt := options["slot"]
		valueOpt := options["value"]
		if slotOpt == nil || valueOpt == nil {
			return editInteractionResponse(s, i, "Missing slot or value.")
		}
		slot := strings.TrimSpace(slotOpt.StringValue())
		value := strings.TrimSpace(valueOpt.StringValue())

		if _, ok := loadoutSettableSlots[slot]; !ok {
			return editInteractionResponse(s, i, fmt.Sprintf("Invalid slot `%s`.", slot))
		}
		if err := validateLoadoutToken(value, maxLoadoutValueLength, "value"); err != nil {
			return editInteractionResponse(s, i, err.Error())
		}

		unlocked, err := svc.UserUnlockedCosmetics(ctx, userID)
		if err != nil {
			return fmt.Errorf("failed to evaluate unlocks: %w", err)
		}

		isUnlocked := unlocked[value]
		if (isKnownVRMLCosmetic(value) || strings.Contains(strings.ToLower(value), "vrml")) && !isUnlocked {
			return editInteractionResponse(s, i, "That VRML cosmetic is not entitled for your account.")
		}

		applyLoadoutSlot(&profile.LoadoutCosmetics.Loadout, slot, value)
		if err := EVRProfileUpdate(ctx, d.nk, userID, profile); err != nil {
			return fmt.Errorf("failed to set loadout slot: %w", err)
		}

		response := fmt.Sprintf("Set `%s` to `%s`.", slot, value)
		if !isUnlocked {
			response += " Warning: this cosmetic is not currently unlocked and may not apply in-game."
		}
		return editInteractionResponse(s, i, response)

	case "remove":
		nameOpt := options["name"]
		if nameOpt == nil {
			return editInteractionResponse(s, i, "Missing loadout name.")
		}
		name := strings.TrimSpace(nameOpt.StringValue())
		if !wardrobe.DeleteOutfit(name) {
			return editInteractionResponse(s, i, fmt.Sprintf("Loadout `%s` does not exist.", name))
		}
		if err := StorableWrite(ctx, d.nk, userID, wardrobe); err != nil {
			return fmt.Errorf("failed to remove loadout: %w", err)
		}
		return editInteractionResponse(s, i, fmt.Sprintf("Removed loadout `%s`.", name))
	default:
		return editInteractionResponse(s, i, "Unknown loadout subcommand.")
	}
}

type loadoutAutocompleteHTTPHandler struct {
	service *LoadoutAutocompleteService
}

func (h *loadoutAutocompleteHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	autocompleteType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	partial := strings.TrimSpace(r.URL.Query().Get("q"))
	slot := strings.TrimSpace(r.URL.Query().Get("slot"))

	ctx := r.Context()
	var (
		choices []*discordgo.ApplicationCommandOptionChoice
		err     error
	)

	switch autocompleteType {
	case "name":
		if userID == "" {
			http.Error(w, "user_id is required for type=name", http.StatusBadRequest)
			return
		}
		choices, err = h.service.NameChoices(ctx, userID, partial)
	case "slot":
		choices = h.service.SlotChoices(partial)
	case "value":
		if userID == "" {
			http.Error(w, "user_id is required for type=value", http.StatusBadRequest)
			return
		}
		if slot == "" {
			http.Error(w, "slot is required for type=value", http.StatusBadRequest)
			return
		}
		choices, err = h.service.ValueChoices(ctx, userID, slot, partial)
	default:
		http.Error(w, "unknown autocomplete type", http.StatusBadRequest)
		return
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": choices})
}

func RegisterLoadoutAutocompleteHandler(initializer runtime.Initializer, service *LoadoutAutocompleteService) error {
	handler := &loadoutAutocompleteHTTPHandler{service: service}
	return initializer.RegisterHttp(loadoutAutocompleteEndpoint, handler.ServeHTTP, http.MethodGet, http.MethodOptions)
}

func isStorageNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	if status.Code(err) == codes.NotFound {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}
