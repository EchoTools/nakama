// purge-docker-ips is a standalone tool that removes Docker internal network IP
// entries (172.18.0.*) from Nakama login histories and rebuilds alternate
// account associations.
//
// Usage:
//
//	GOWORK=off go run ./tools/purge-docker-ips --dsn "postgres://nakama:localdb@127.0.0.1:5432/nakama" --dry-run
//	GOWORK=off go run ./tools/purge-docker-ips --dsn "postgres://nakama:localdb@127.0.0.1:5432/nakama"
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"slices"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/heroiclabs/nakama/v3/server"
)

// Type aliases for server types
type (
	LoginHistory          = server.LoginHistory
	LoginHistoryEntry     = server.LoginHistoryEntry
	AlternateSearchMatch  = server.AlternateSearchMatch
	StorableMetadata      = server.StorableMetadata
)

const MaxCacheSize = 10000

// ---------------------------------------------------------------------------
// IP matching helpers
// ---------------------------------------------------------------------------

const dockerSubnet = "172.18.0.0/16"

var dockerNet *net.IPNet

func init() {
	_, dockerNet, _ = net.ParseCIDR(dockerSubnet)
}

func isDockerIP(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	return dockerNet.Contains(parsed)
}

// ---------------------------------------------------------------------------
// matchIgnoredAltPattern (mirrors server/evr_authenticate_history.go)
// ---------------------------------------------------------------------------

var IgnoredLoginValues = map[string]struct{}{
	"":                {},
	"1WMHH000X00000":  {},
	"N/A":             {},
	"UNK-0":           {},
	"OVR-ORG-0":       {},
	"unknown":         {},
	"1PASH5D1P17365":  {},
	"WMHD315M3010GV":  {},
	"VRLINKHMDQUEST":  {},
	"VRLINKHMDQUEST2": {},
	"VRLINKHMDQUEST3": {},
}

func matchIgnoredAltPattern(pattern string) bool {
	if _, ok := IgnoredLoginValues[pattern]; ok {
		return true
	} else if ip := net.ParseIP(pattern); ip != nil {
		if ip.IsPrivate() {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// rebuildCache (mirrors server/evr_authenticate_history.go rebuildCache)
// ---------------------------------------------------------------------------

func rebuildCache(h *server.LoginHistory) {
	historyLen := len(h.History)
	h.Cache = make([]string, 0, historyLen*4)
	h.XPIs = make(map[string]time.Time, historyLen)
	h.ClientIPs = make(map[string]time.Time, historyLen)

	cacheSet := make(map[string]bool, historyLen*4)

	for _, e := range h.History {
		for _, s := range e.Items() {
			if _, found := cacheSet[s]; !found && !matchIgnoredAltPattern(s) {
				h.Cache = append(h.Cache, s)
				cacheSet[s] = true
			}
		}

		if !e.XPID.IsNil() {
			evrIDStr := e.XPID.String()
			if t, found := h.XPIs[evrIDStr]; !found || t.Before(e.UpdatedAt) {
				h.XPIs[evrIDStr] = e.UpdatedAt
			}
		}

		if e.ClientIP != "" {
			if t, found := h.ClientIPs[e.ClientIP]; !found || t.Before(e.UpdatedAt) {
				h.ClientIPs[e.ClientIP] = e.UpdatedAt
			}
		}
	}

	if h.DeniedClientAddresses != nil {
		for _, addr := range h.DeniedClientAddresses {
			if _, found := cacheSet[addr]; !found {
				h.Cache = append(h.Cache, addr)
				cacheSet[addr] = true
			}
		}
	}

	slices.Sort(h.Cache)
	h.Cache = slices.Compact(h.Cache)

	if len(h.Cache) > MaxCacheSize {
		h.Cache = h.Cache[:MaxCacheSize]
	}
}

// ---------------------------------------------------------------------------
// loginHistoryCompare (mirrors server/evr_authenticate_alts.go)
// Item order: [XPID, ClientIP, SystemProfile, HMDSerial]
// ---------------------------------------------------------------------------

func loginHistoryCompare(aUserID string, a *server.LoginHistory, bUserID string, b *server.LoginHistory) []*server.AlternateSearchMatch {
	if aUserID == bUserID {
		return nil
	}
	if a == nil || b == nil || len(a.History) == 0 || len(b.History) == 0 {
		return nil
	}

	authUserData := make([][][]string, 2)
	for i, hist := range []map[string]*server.LoginHistoryEntry{a.History, b.History} {
		authUserData[i] = make([][]string, 0, len(hist))
		for _, e := range hist {
			sp := ""
			hmd := ""
			if e.LoginData != nil {
				sp = e.SystemProfile()
				hmd = e.LoginData.HMDSerialNumber
			}
			items := []string{
				e.XPID.String(),
				e.ClientIP,
				sp,
				hmd,
			}
			authUserData[i] = append(authUserData[i], items)
		}
	}

	matches := make([]*AlternateSearchMatch, 0)
	for _, itemsA := range authUserData[0] {
		for _, itemsB := range authUserData[1] {
			matchingItems := make([]string, 0, len(itemsA))
			for i, item := range itemsA {
				if item == itemsB[i] && item != "" {
					matchingItems = append(matchingItems, item)
				}
			}
			if len(matchingItems) > 0 {
				matches = append(matches, &AlternateSearchMatch{
					OtherUserID: bUserID,
					Items:       matchingItems,
				})
			}
		}
	}
	return matches
}

// ---------------------------------------------------------------------------
// Storage row
// ---------------------------------------------------------------------------

type storageRow struct {
	UserID  string
	Value   string
	Version string
}

// ---------------------------------------------------------------------------
// Stats
// ---------------------------------------------------------------------------

type stats struct {
	totalUsers           int
	usersModified        int
	historyEntriesPurged int
	activeEntriesPurged  int
	authorizedIPsPurged  int
	clientIPsPurged      int
	pendingAuthsPurged   int
	deniedAddrsPurged    int
	alternatesPurged     int
	altMatchesBefore     int
	altMatchesAfter      int
	errors               int
}

func (s *stats) Print() {
	fmt.Println("╔══════════════════════════════════════════════════════╗")
	fmt.Println("║               PURGE DOCKER IPS - RESULTS            ║")
	fmt.Println("╠══════════════════════════════════════════════════════╣")
	fmt.Printf("║  Total users scanned           : %7d             ║\n", s.totalUsers)
	fmt.Printf("║  Users modified                : %7d             ║\n", s.usersModified)
	fmt.Printf("║  History entries purged         : %7d             ║\n", s.historyEntriesPurged)
	fmt.Printf("║  Active entries purged          : %7d             ║\n", s.activeEntriesPurged)
	fmt.Printf("║  Authorized IPs purged          : %7d             ║\n", s.authorizedIPsPurged)
	fmt.Printf("║  Client IPs purged              : %7d             ║\n", s.clientIPsPurged)
	fmt.Printf("║  Pending authorizations purged  : %7d             ║\n", s.pendingAuthsPurged)
	fmt.Printf("║  Denied addresses purged        : %7d             ║\n", s.deniedAddrsPurged)
	fmt.Printf("║  Alternate matches (before)     : %7d             ║\n", s.altMatchesBefore)
	fmt.Printf("║  Alternate matches cleared      : %7d             ║\n", s.alternatesPurged)
	fmt.Printf("║  Errors                         : %7d             ║\n", s.errors)
	fmt.Println("╚══════════════════════════════════════════════════════╝")
}

// ---------------------------------------------------------------------------
// MAIN
// ---------------------------------------------------------------------------

func main() {
	dsn := flag.String("dsn", "", "PostgreSQL DSN (e.g. postgres://nakama:localdb@127.0.0.1:5432/nakama)")
	dryRun := flag.Bool("dry-run", false, "Show what would change without writing anything")
	ipPattern := flag.String("ip-pattern", "172.18.0.", "IP prefix to purge (default: 172.18.0.)")
	verbose := flag.Bool("verbose", false, "Print per-user details")
	flag.Parse()

	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "Error: --dsn is required")
		flag.Usage()
		os.Exit(1)
	}

	if *dryRun {
		fmt.Println("*** DRY RUN MODE — no changes will be written ***")
		fmt.Println()
	}

	ctx := context.Background()

	log.Println("Connecting to database...")
	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}
	log.Println("Connected.")

	// ===================================================================
	// Phase 1: Load all login histories, purge docker IPs, rebuild caches
	// ===================================================================
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Println("  Phase 1: Purge Docker IP entries & rebuild caches")
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Println()

	rows, err := db.QueryContext(ctx,
		`SELECT user_id, value, version
		   FROM storage
		  WHERE collection = 'Login' AND key = 'history'
		  ORDER BY user_id`)
	if err != nil {
		log.Fatalf("Failed to query login histories: %v", err)
	}

	var allRows []storageRow
	for rows.Next() {
		var r storageRow
		if err := rows.Scan(&r.UserID, &r.Value, &r.Version); err != nil {
			log.Fatalf("Failed to scan row: %v", err)
		}
		allRows = append(allRows, r)
	}
	rows.Close()

	log.Printf("Loaded %d login history records", len(allRows))

	s := &stats{totalUsers: len(allRows)}

	type cleanedRecord struct {
		UserID  string
		History *server.LoginHistory
		Version string
		Dirty   bool
	}
	cleaned := make([]cleanedRecord, 0, len(allRows))

	for _, row := range allRows {
		var h server.LoginHistory
		if err := json.Unmarshal([]byte(row.Value), &h); err != nil {
			log.Printf("ERROR: Failed to unmarshal for user %s: %v", row.UserID, err)
			s.errors++
			continue
		}
		h.SetStorageMeta(server.StorableMetadata{
			UserID:  row.UserID,
			Version: row.Version,
		})

		dirty := false
		var userLog strings.Builder
		if *verbose {
			fmt.Fprintf(&userLog, "User %s:\n", row.UserID)
		}

		// --- History ---
		for key, entry := range h.History {
			if strings.HasPrefix(entry.ClientIP, *ipPattern) || isDockerIP(entry.ClientIP) {
				if *verbose {
					fmt.Fprintf(&userLog, "  [History] key=%s ip=%s xpid=%s\n", key, entry.ClientIP, entry.XPID.Token())
				}
				delete(h.History, key)
				s.historyEntriesPurged++
				dirty = true
			}
		}

		// --- Active ---
		for key, entry := range h.Active {
			if strings.HasPrefix(entry.ClientIP, *ipPattern) || isDockerIP(entry.ClientIP) {
				if *verbose {
					fmt.Fprintf(&userLog, "  [Active] key=%s ip=%s\n", key, entry.ClientIP)
				}
				delete(h.Active, key)
				s.activeEntriesPurged++
				dirty = true
			}
		}

		// --- AuthorizedIPs ---
		for ip := range h.AuthorizedIPs {
			if strings.HasPrefix(ip, *ipPattern) || isDockerIP(ip) {
				if *verbose {
					fmt.Fprintf(&userLog, "  [AuthorizedIPs] ip=%s\n", ip)
				}
				delete(h.AuthorizedIPs, ip)
				s.authorizedIPsPurged++
				dirty = true
			}
		}

		// --- ClientIPs ---
		for ip := range h.ClientIPs {
			if strings.HasPrefix(ip, *ipPattern) || isDockerIP(ip) {
				if *verbose {
					fmt.Fprintf(&userLog, "  [ClientIPs] ip=%s\n", ip)
				}
				delete(h.ClientIPs, ip)
				s.clientIPsPurged++
				dirty = true
			}
		}

		// --- PendingAuthorizations ---
		for key, entry := range h.PendingAuthorizations {
			if strings.HasPrefix(key, *ipPattern) || isDockerIP(key) ||
				(entry != nil && (strings.HasPrefix(entry.ClientIP, *ipPattern) || isDockerIP(entry.ClientIP))) {
				if *verbose {
					clientIP := ""
					if entry != nil {
						clientIP = entry.ClientIP
					}
					fmt.Fprintf(&userLog, "  [PendingAuth] key=%s ip=%s\n", key, clientIP)
				}
				delete(h.PendingAuthorizations, key)
				s.pendingAuthsPurged++
				dirty = true
			}
		}

		// --- DeniedClientAddresses ---
		newDenied := make([]string, 0, len(h.DeniedClientAddresses))
		for _, addr := range h.DeniedClientAddresses {
			if strings.HasPrefix(addr, *ipPattern) || isDockerIP(addr) {
				if *verbose {
					fmt.Fprintf(&userLog, "  [DeniedAddrs] addr=%s\n", addr)
				}
				s.deniedAddrsPurged++
				dirty = true
			} else {
				newDenied = append(newDenied, addr)
			}
		}
		h.DeniedClientAddresses = newDenied

		// --- Count existing alternates ---
		prevAltCount := len(h.AlternateMatches)
		s.altMatchesBefore += prevAltCount

		// --- Purge docker IPs from AlternateMatches items ---
		if dirty && len(h.AlternateMatches) > 0 {
			for otherUserID, matches := range h.AlternateMatches {
				remaining := make([]*AlternateSearchMatch, 0, len(matches))
				for _, m := range matches {
					cleanItems := make([]string, 0, len(m.Items))
					for _, item := range m.Items {
						if !strings.HasPrefix(item, *ipPattern) && !isDockerIP(item) {
							cleanItems = append(cleanItems, item)
						}
					}
					if len(cleanItems) > 0 {
						m.Items = cleanItems
						remaining = append(remaining, m)
					}
				}
				if len(remaining) == 0 {
					if *verbose {
						fmt.Fprintf(&userLog, "  [Alternates] Removing %s (only linked by docker IP)\n", otherUserID)
					}
					delete(h.AlternateMatches, otherUserID)
					s.alternatesPurged++
				} else {
					h.AlternateMatches[otherUserID] = remaining
				}
			}
		}

		// --- Rebuild cache from remaining history ---
		if dirty {
			rebuildCache(&h)
		}

		if *verbose && dirty {
			fmt.Print(userLog.String())
			fmt.Printf("  → %d alt matches remaining (was %d)\n\n", len(h.AlternateMatches), prevAltCount)
		}

		s.altMatchesAfter += len(h.AlternateMatches)
		if dirty {
			s.usersModified++
		}

		cleaned = append(cleaned, cleanedRecord{
			UserID:  row.UserID,
			History: &h,
			Version: row.Version,
			Dirty:   dirty,
		})
	}

	fmt.Println()
	fmt.Println("Phase 1 summary:")
	s.Print()

	// ===================================================================
	// Phase 2: Write back purged records
	// ===================================================================
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Println("  Phase 2: Write back purged records")
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Println()

	if *dryRun {
		fmt.Printf("DRY RUN: Would update %d user records.\n", s.usersModified)
	} else {
		writeCount := 0
		writeErrors := 0
		for _, rec := range cleaned {
			if !rec.Dirty {
				continue
			}
			data, err := json.Marshal(rec.History)
			if err != nil {
				log.Printf("ERROR: marshal user %s: %v", rec.UserID, err)
				writeErrors++
				continue
			}
			result, err := db.ExecContext(ctx,
				`UPDATE storage
				    SET value = $1,
				        version = REPLACE(gen_random_uuid()::TEXT, '-', ''),
				        update_time = now()
				  WHERE collection = 'Login'
				    AND key = 'history'
				    AND user_id = $2
				    AND version = $3`,
				string(data), rec.UserID, rec.Version)
			if err != nil {
				log.Printf("ERROR: update user %s: %v", rec.UserID, err)
				writeErrors++
				continue
			}
			affected, _ := result.RowsAffected()
			if affected == 0 {
				log.Printf("WARN: version conflict for user %s (record changed since read)", rec.UserID)
				writeErrors++
				continue
			}
			writeCount++
			if writeCount%100 == 0 {
				log.Printf("Written %d/%d records...", writeCount, s.usersModified)
			}
		}
		fmt.Printf("\nPhase 2 writes: %d OK, %d errors\n", writeCount, writeErrors)
	}

	// ===================================================================
	// Phase 3: Clean alternate associations for affected users
	// ===================================================================
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Println("  Phase 3: Clean alternate associations")
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Println()

	// Index all histories by user ID
	historyByUser := make(map[string]*LoginHistory, len(cleaned))
	recByUser := make(map[string]*cleanedRecord, len(cleaned))
	purgedUserIDs := make(map[string]bool)
	for i := range cleaned {
		historyByUser[cleaned[i].UserID] = cleaned[i].History
		recByUser[cleaned[i].UserID] = &cleaned[i]
		if cleaned[i].Dirty {
			purgedUserIDs[cleaned[i].UserID] = true
		}
	}

	// For each purged user, re-validate their AlternateMatches.
	// Also find OTHER users who reference a purged user and re-validate those too.
	affectedUsers := make(map[string]bool)
	for uid := range purgedUserIDs {
		affectedUsers[uid] = true
	}
	// Find users that list a purged user as an alternate
	for i := range cleaned {
		h := cleaned[i].History
		for altID := range h.AlternateMatches {
			if purgedUserIDs[altID] {
				affectedUsers[cleaned[i].UserID] = true
				break
			}
		}
	}

	log.Printf("Phase 3: %d purged users, %d total affected (including neighbors)", len(purgedUserIDs), len(affectedUsers))

	rebuiltCount := 0
	altsRemoved := 0

	for uid := range affectedUsers {
		rec, ok := recByUser[uid]
		if !ok {
			continue
		}
		h := rec.History

		oldAltCount := len(h.AlternateMatches)

		// For each existing alternate match, re-validate against their
		// current (possibly purged) history. Remove stale ones.
		for otherUID, matches := range h.AlternateMatches {
			otherH, ok := historyByUser[otherUID]
			if !ok {
				// Other user not found — remove reference
				delete(h.AlternateMatches, otherUID)
				continue
			}

			// Re-run comparison
			newMatches := loginHistoryCompare(uid, h, otherUID, otherH)
			if len(newMatches) == 0 {
				delete(h.AlternateMatches, otherUID)
			} else {
				h.AlternateMatches[otherUID] = newMatches
			}
			_ = matches
		}

		// Check: did anything change?
		newAltCount := len(h.AlternateMatches)

		if newAltCount != oldAltCount {
			rec.Dirty = true
			rebuiltCount++
			removed := 0
			if oldAltCount > newAltCount {
				removed = oldAltCount - newAltCount
			}
			altsRemoved += removed
			if *verbose {
				fmt.Printf("User %s: alternates %d → %d (removed %d)\n",
					uid, oldAltCount, newAltCount, removed)
			}
		}
	}

	fmt.Printf("Alternates checked for %d affected users, %d users changed, %d associations removed\n",
		len(affectedUsers), rebuiltCount, altsRemoved)

	// ===================================================================
	// Phase 4: Write back rebuilt alternates
	// ===================================================================
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Println("  Phase 4: Write back rebuilt alternate associations")
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Println()

	if *dryRun {
		dirtyCount := 0
		for i := range cleaned {
			if cleaned[i].Dirty {
				dirtyCount++
			}
		}
		fmt.Printf("DRY RUN: Would update %d total user records (purged + alternate rebuild).\n", dirtyCount)
	} else {
		writeCount := 0
		writeErrors := 0
		for i := range cleaned {
			rec := &cleaned[i]
			if !rec.Dirty {
				continue
			}

			data, err := json.Marshal(rec.History)
			if err != nil {
				log.Printf("ERROR: marshal user %s: %v", rec.UserID, err)
				writeErrors++
				continue
			}

			// Read current version to handle phase-2 version bumps
			var currentVersion string
			err = db.QueryRowContext(ctx,
				`SELECT version FROM storage
				  WHERE collection = 'Login' AND key = 'history' AND user_id = $1`,
				rec.UserID).Scan(&currentVersion)
			if err != nil {
				log.Printf("ERROR: read version user %s: %v", rec.UserID, err)
				writeErrors++
				continue
			}

			result, err := db.ExecContext(ctx,
				`UPDATE storage
				    SET value = $1,
				        version = REPLACE(gen_random_uuid()::TEXT, '-', ''),
				        update_time = now()
				  WHERE collection = 'Login'
				    AND key = 'history'
				    AND user_id = $2
				    AND version = $3`,
				string(data), rec.UserID, currentVersion)
			if err != nil {
				log.Printf("ERROR: update user %s: %v", rec.UserID, err)
				writeErrors++
				continue
			}

			affected, _ := result.RowsAffected()
			if affected == 0 {
				log.Printf("WARN: version conflict for user %s", rec.UserID)
				writeErrors++
				continue
			}

			writeCount++
			if writeCount%100 == 0 {
				log.Printf("Written %d records...", writeCount)
			}
		}
		fmt.Printf("\nPhase 4 writes: %d OK, %d errors\n", writeCount, writeErrors)
	}

	fmt.Println()
	fmt.Println("Done!")
}
