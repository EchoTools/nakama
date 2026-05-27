# EchoTools/nakama — Agent Instructions

## Standards

This project adopts **`/srv/src/metis-core/GO-ADDENDUM-GENERIC.md`** as the
binding Go code standard. Read it before writing or reviewing code.

Key requirements for any agent (including heisthecat31 or any AI assistant):

### You MUST run before committing

```bash
gofmt -l -w    # format
go vet ./...   # static analysis
golangci-lint run  # comprehensive lint
go test -race ./server/...  # tests with race detector
go fix ./...   # apply modernizers
go mod tidy    # clean dependencies
govulncheck    # vulnerability check
```

### Pre-push hook (automated gate)

The repo ships a pre-push hook in `.githooks/pre-push` that checks:
1. Tag format (`v*` tags must include `-evr.<N>`)
2. `gofmt` compliance
3. `go vet` on changed packages
4. `gopls` diagnostics on changed files
5. `go mod tidy` hasn't drifted

Install: `git config core.hooksPath .githooks`

### Architecture rules

- **`server/evr_*.go`** is custom EchoVR code. Upstream Nakama code is rarely modified.
- **Symbol hash** is CSymbol64 (see `server/evr/core_hash.go`), NOT any other hash.
- **Matchmaker** is the most complex subsystem — changes need integration tests.
- **Party follow** path (`TryFollowPartyLeader` / `pollFollowPartyLeader`) has 30+ unit tests.
- **Display names** have three systems (Nakama, EchoVR in-game, Discord) that interact in
  non-obvious ways.
- **Log discipline**: expected behavior is `info` or `debug`. `warn` = someone should look.
  `error` = something broke. Never downgrade `warn` to `debug` without explicit review.

### Hard invariants (DO NOT CHANGE)

- **Guild isolation is absolute.** All matchmaking streams, tickets, queries, and lobby
  searches are scoped to `GroupID`. Players in different guilds NEVER match together,
  even in public modes. Do NOT normalize, nil-out, or bypass GroupID for "cross-guild"
  matching. This has been incorrectly "fixed" multiple times. It is not a bug.

### Common violations to flag on sight

- Removing logging (debug/warn/error) without explanation
- Adding nil checks that mask the real bug instead of fixing it
- Fixes that touch multiple subsystems when the root cause is in one place
- Commits without test changes for logic modifications
- Parallel goroutine joins to the same match (use sequential with early termination)
- Hardcoded Symbol values instead of `ToSymbol()`
- Matchmaker changes without running integration tests
