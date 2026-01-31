#!/bin/bash
# Helper script to delete merged branches using GitHub API
# This script can be run by anyone with appropriate permissions

set -e

OWNER="EchoTools"
REPO="nakama"

# Branches to delete
branches=(
    "copilot/refactor-server-issue-reports"
    "copilot/remove-obsolete-portal-directory"
    "copilot/search-matchmaker-bug-fixes"
    "feat/bot-testing-global-developers"
    "feat/early-quit-lockout-system"
    "feat/early-quit-notifications"
    "feat/early-quit-unified"
    "feat/matchmaker-candidate-guard"
    "fix/matchmaker-memory-explosion"
    "refactor/earlyquit-client-side-enforcement"
)

# Check if gh CLI is authenticated
if ! gh auth status &>/dev/null; then
    echo "ERROR: GitHub CLI is not authenticated."
    echo "Please run: gh auth login"
    exit 1
fi

echo "Deleting ${#branches[@]} merged branches from $OWNER/$REPO..."
echo ""

deleted=0
not_found=0
errors=0

for branch in "${branches[@]}"; do
    echo -n "Deleting $branch... "
    
    # Use GitHub CLI to delete the branch
    if gh api -X DELETE "/repos/$OWNER/$REPO/git/refs/heads/$branch" 2>/dev/null; then
        echo "✓ Deleted"
        ((deleted++))
    else
        # Check if branch exists
        if gh api "/repos/$OWNER/$REPO/git/ref/heads/$branch" &>/dev/null; then
            echo "✗ Failed (permission or other error)"
            ((errors++))
        else
            echo "⊘ Not found (already deleted)"
            ((not_found++))
        fi
    fi
done

echo ""
echo "Summary:"
echo "  Successfully deleted: $deleted"
echo "  Not found: $not_found"
echo "  Errors: $errors"
echo ""

if [ $deleted -gt 0 ] || [ $not_found -gt 0 ]; then
    echo "Remaining branches in repository:"
    gh api "/repos/$OWNER/$REPO/branches" --paginate | jq -r '.[].name' | sort
fi
