# Delete Merged Branches

This document contains the list of branches that should be deleted from the EchoTools/nakama repository.

## Branches to Delete

The following branches have been identified as merged or from closed PRs and should be deleted:

1. `copilot/refactor-server-issue-reports`
2. `copilot/remove-obsolete-portal-directory`
3. `copilot/search-matchmaker-bug-fixes`
4. `feat/bot-testing-global-developers`
5. `feat/early-quit-lockout-system`
6. `feat/early-quit-notifications`
7. `feat/early-quit-unified`
8. `feat/matchmaker-candidate-guard`
9. `fix/matchmaker-memory-explosion`
10. `refactor/earlyquit-client-side-enforcement`

## How to Delete

### Option 1: Using GitHub CLI (gh)

```bash
# Authenticate with GitHub
gh auth login

# Delete branches one by one
gh api -X DELETE /repos/EchoTools/nakama/git/refs/heads/copilot/refactor-server-issue-reports
gh api -X DELETE /repos/EchoTools/nakama/git/refs/heads/copilot/remove-obsolete-portal-directory
gh api -X DELETE /repos/EchoTools/nakama/git/refs/heads/copilot/search-matchmaker-bug-fixes
gh api -X DELETE /repos/EchoTools/nakama/git/refs/heads/feat/bot-testing-global-developers
gh api -X DELETE /repos/EchoTools/nakama/git/refs/heads/feat/early-quit-lockout-system
gh api -X DELETE /repos/EchoTools/nakama/git/refs/heads/feat/early-quit-notifications
gh api -X DELETE /repos/EchoTools/nakama/git/refs/heads/feat/early-quit-unified
gh api -X DELETE /repos/EchoTools/nakama/git/refs/heads/feat/matchmaker-candidate-guard
gh api -X DELETE /repos/EchoTools/nakama/git/refs/heads/fix/matchmaker-memory-explosion
gh api -X DELETE /repos/EchoTools/nakama/git/refs/heads/refactor/earlyquit-client-side-enforcement
```

### Option 2: Using git with credentials

```bash
# Make sure you have push access to the repository
git push origin --delete copilot/refactor-server-issue-reports
git push origin --delete copilot/remove-obsolete-portal-directory
git push origin --delete copilot/search-matchmaker-bug-fixes
git push origin --delete feat/bot-testing-global-developers
git push origin --delete feat/early-quit-lockout-system
git push origin --delete feat/early-quit-notifications
git push origin --delete feat/early-quit-unified
git push origin --delete feat/matchmaker-candidate-guard
git push origin --delete fix/matchmaker-memory-explosion
git push origin --delete refactor/earlyquit-client-side-enforcement
```

### Option 3: Using the GitHub Web UI

1. Go to https://github.com/EchoTools/nakama/branches
2. Find each branch in the list above
3. Click the trash icon next to the branch name to delete it

## Verification

After deletion, verify that the branches are gone:

```bash
git ls-remote --heads origin | grep -E "(copilot/refactor-server-issue-reports|copilot/remove-obsolete-portal-directory|copilot/search-matchmaker-bug-fixes|feat/bot-testing-global-developers|feat/early-quit-lockout-system|feat/early-quit-notifications|feat/early-quit-unified|feat/matchmaker-candidate-guard|fix/matchmaker-memory-explosion|refactor/earlyquit-client-side-enforcement)"
```

The command should return no results if all branches have been successfully deleted.

## Analysis

These branches were identified through the following process:

1. Listed all remote branches in the repository (51 total)
2. Retrieved all closed PRs from the repository
3. Identified branches that were:
   - Merged into main, OR
   - Associated with closed PRs
4. Excluded the `main` branch and current working branch

This cleanup will help keep the repository organized and remove stale branches that are no longer needed.
