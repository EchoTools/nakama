---
description: 'Project management framework for the EVR module extraction. Agents must follow this workflow for all refactoring sessions.'
applyTo: '**'
---

# Project Management — EVR Module Extraction

This project refactors the nakama EVR fork by extracting all EchoVR-specific
code into a standalone Go module (`github.com/echotools/nakama-evr`).

**THIS IS NOT A FREE-FORM REFACTOR.** The project is managed through a
structured plan with verification gates between phases. Every agent session
must follow the READ → DO → VERIFY → UPDATE → LOOP workflow.

---

## The Workflow (Mandatory. No Exceptions.)

Every agent session follows exactly this loop:

```
1. READ   → Load REFACTOR.yaml. Determine current phase and next unstarted task.
2. PLAN   → Before touching any code, state what you intend to do and verify.
3. DO     → Execute the task.
4. VERIFY → Run all verification steps for the task. If ANY fail, STOP.
5. UPDATE → Update REFACTOR.yaml with results.
6. LOOP   → If more tasks remain, go to step 1. If phase is done, signal completion.
```

### Step 1: READ — Orient in the Plan

Read `REFACTOR.yaml` at the repo root **before anything else**.

```yaml
# What you're looking for:
# - project.status → what state the overall project is in
# - phases[].status → what state each phase is in
# - phases[].tasks[].status → what tasks are done vs pending
# - blockers → anything currently blocking progress
# - notes → recent agent communications
```

Find the **current active phase** (first phase whose status is not
`completed`). Within that phase, find the **first unstarted task**.

If the current phase is `blocked`, read the blockers list. Do not proceed
until the blocker is resolved. If you can resolve it, do so and mark it
cleared. If you cannot, add a note and **stop**.

### Step 2: PLAN — State Your Intent

Before writing any code, state:

```
I am working on task X.Y (description).
My approach is:
1. ...
2. ...
3. ...

I will verify by:
- make nakama still compiles
- go test ./... passes
- [any task-specific verification]
```

**If you cannot clearly state your plan, do not start the task.** Something
is underspecified. Add a note to REFACTOR.yaml and stop.

### Step 3: DO — Execute

Write the code. Follow these constraints:

- **ALWAYS use `make nakama` to build.** Never `go build` directly.
- **Run `go mod tidy && go mod vendor` after dependency changes.**
- **One file at a time** for extraction work. Verify after each file.
- **Use thin wrappers/aliases** in the original location when extracting —
  the original nakama code imports `server/evr_*`, so you need an alias shim
  that re-exports from the new module. This keeps nakama compiling during the
  transition.
- **Do NOT delete original files until phase 10** (cleanup phase). Only add
  alias files that delegate to the new module.

#### Alias Shim Pattern

When you extract `server/evr_foo.go` to the new module:

```go
// server/evr_foo.go → becomes a thin alias:
package server

import (
    evr "github.com/echotools/nakama-evr/server"
)

// Re-export everything the original exported
var NewFoo = evr.NewFoo
type Foo = evr.Foo
// ... etc
```

This keeps nakama compiling without changing any importers yet. In phase 10,
you remove these aliases and update importers directly.

### Step 4: VERIFY — Prove It Works

**Every task has a verification checklist.** Run ALL of these before
marking anything done:

#### Universal Verification (Every Task)

```bash
# 1. Build nakama (MUST pass)
make nakama

# 2. Run vet
go vet ./...

# 3. Run linter
golangci-lint run ./...

# 4. Run tests for affected packages
go test -race ./server/...  # or the specific package

# 5. Run go fix and mod tidy (must be clean)
go fix ./... && go mod tidy && go mod vendor

# 6. Verify no new warnings
go vet ./... 2>&1 | grep -v "^#" || true
```

#### Extraction-Specific Verification

```
✓ New module compiles with go build
✓ New module tests pass
✓ Nakama still compiles (with aliases)
✓ Nakama tests still pass
✓ No duplicated symbols (go vet catches this)
✓ .smell-scan-manifest.json hashes are updated for modified files
```

#### If Verification Fails

1. **STOP.** Do not mark the task complete.
2. Diagnose the failure.
3. If it's a simple fix (wrong import, missing export), fix it and re-verify.
4. If it's a structural problem (circular dependency, missing interface):
   a. Add a note to REFACTOR.yaml describing the problem.
   b. Update the plan if the approach needs to change.
   c. Do NOT work around it with hacks. Stop and signal.
5. If you cannot fix it in 3 attempts, add a blocker in REFACTOR.yaml and
   stop the session.

### Step 5: UPDATE — Record Progress

After successful verification, update `REFACTOR.yaml`:

```python
# Use this exact Python snippet to update task status:
python3 -c "
import yaml, sys
with open('REFACTOR.yaml') as f:
    plan = yaml.safe_load(f)
# Find and update the relevant task
for phase in plan['phases']:
    for task in phase['tasks']:
        if task['id'] == 'X.Y':
            task['status'] = 'completed'
            break
# Check if all tasks in phase are complete
all_done = all(t['status'] == 'completed' for t in phase['tasks'])
if all_done:
    phase['status'] = 'completed'
# Add a note
plan['notes'].append({
    'timestamp': '$(date -u +%Y-%m-%dT%H:%M:%SZ)',
    'agent': 'copilot-cloud-agent',
    'message': 'Completed task X.Y: description of what was done'
})
with open('REFACTOR.yaml', 'w') as f:
    yaml.dump(plan, f, default_flow_style=False)
"
```

**Do NOT skip this step.** The next agent depends on accurate state.

### Step 6: LOOP — Continue or Signal

- If the current phase has more unstarted tasks → return to step 1.
- If the current phase is now completed → check if the next phase can start
  (no blockers, completion criteria met). If yes, update the next phase's
  status to `in_progress` and start its first task.
- If all phases are completed → signal project completion.
- If blocked → write a clear note describing what's needed and stop.

---

## Phase Transitions

Between phases, run the **integration verification**:

```bash
# Full nakama build
make nakama

# Full test suite
go test -race -count=1 ./...

# Format and lint
gofmt -w . && goimports -w . && golangci-lint run ./...

# Clean module state
go mod tidy && go mod vendor
```

Phase 2 ("define module boundary & interfaces") requires **human review**
before proceeding to phase 3. Set a blocker and stop after phase 2
completion. Do not proceed past phase 2 without Andrew confirming the
interface design.

---

## Rollback Protocol

If a change breaks something and you cannot fix it:

1. **Restore from git:** `git checkout -- path/to/file.go`
2. **If you already committed:** `git revert HEAD` (for the last commit) or
   `git revert <hash>` (for an older commit).
3. **Update the plan:** Set the task status back to `not_started`.
4. **Add a note** describing what went wrong and why.
5. **Try a different approach** if you have one. Otherwise, stop.

The project uses atomic commits per task. Each task's changes should be in
a single commit. This makes rollback straightforward.

---

## Commit Convention

Every commit during this refactor must follow:

```
refactor(extract): <component> — <what changed>

Phase: <N>
Task: <N.M>
Verification: make nakama | go test | go vet | golangci-lint

<details if needed>
```

Example:
```
refactor(extract): evr pipeline types — move Symbol/Stream to nakama-evr module

Phase: 4
Task: 4.1
Verification: make nakama | go test ./server/evr/...
```

---

## Agent Handoff Protocol

When one agent session ends and another will continue:

1. **All work is committed.** No uncommitted changes in the working tree.
2. **REFACTOR.yaml is updated** with accurate task status.
3. **Notes are written** describing the current state, any surprises, and
   what the next agent should do first.
4. **No pending verification failures.** If something was broken, it's
   documented as a blocker.

---

## Blocker Communication

If you encounter a blocker:

```yaml
# Add to REFACTOR.yaml blockers list:
- id: "B-<date>-<seq>"
  description: "What's wrong"
  caused_by: "Task X.Y"
  impact: "Cannot proceed with tasks X.Z, X.W"
  suggested_resolution: "What someone needs to do to unblock"
```

After adding a blocker, set the current phase status to `blocked` and stop.

---

## Summary: Agent Session Checklist

```
□ 1. Read REFACTOR.yaml (orient in the plan)
□ 2. Identify current phase and next task
□ 3. Plan approach verbally before coding
□ 4. Execute task (one file at a time for extractions)
□ 5. Run ALL verification steps
□ 6. If verification fails: diagnose → fix → re-verify → or STOP
□ 7. Update REFACTOR.yaml task status
□ 8. Update .smell-scan-manifest.json if .go files changed
□ 9. Commit changes with conventional message
□ 10. LOOP or signal completion
```

**If at any point you are unsure: STOP and add a note.**
This project is too large to guess. The plan is your map — if the map is
wrong, update the map before proceeding.
