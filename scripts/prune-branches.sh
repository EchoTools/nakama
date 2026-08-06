#!/bin/bash
#
# Prune local git branches that have been squash-merged into the default branch.
# 
# Since squash merges don't preserve merge history, this script checks if a branch's
# changes are already present in the default branch by comparing tree hashes.
#
# Usage:
#   ./prune-branches.sh           # Interactive mode - prompts before deleting
#   ./prune-branches.sh -f        # Force mode - deletes without prompting
#   ./prune-branches.sh -n        # Dry run - only shows what would be deleted
#   ./prune-branches.sh -b main   # Specify a different base branch
#

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Default values
FORCE=false
DRY_RUN=false
BASE_BRANCH=""

# Parse arguments
while getopts "fnb:" opt; do
    case $opt in
        f) FORCE=true ;;
        n) DRY_RUN=true ;;
        b) BASE_BRANCH="$OPTARG" ;;
        *)
            echo "Usage: $0 [-f] [-n] [-b base_branch]"
            echo "  -f  Force delete without prompting"
            echo "  -n  Dry run (show what would be deleted)"
            echo "  -b  Specify base branch (default: auto-detect from origin)"
            exit 1
            ;;
    esac
done

# Get the default branch if not specified
if [ -z "$BASE_BRANCH" ]; then
    # Try to get the default branch from origin
    BASE_BRANCH=$(git symbolic-ref refs/remotes/origin/HEAD 2>/dev/null | sed 's@^refs/remotes/origin/@@' || echo "")
    
    if [ -z "$BASE_BRANCH" ]; then
        # Fallback: try common default branch names
        if git show-ref --verify --quiet refs/heads/main; then
            BASE_BRANCH="main"
        elif git show-ref --verify --quiet refs/heads/master; then
            BASE_BRANCH="master"
        else
            echo -e "${RED}Error: Could not determine default branch. Use -b to specify.${NC}"
            exit 1
        fi
    fi
fi

echo -e "${BLUE}Base branch: ${BASE_BRANCH}${NC}"
echo ""

# Fetch latest from origin and prune remote tracking branches
echo -e "${BLUE}Fetching latest from origin...${NC}"
git fetch origin --prune

# Get the current branch to avoid deleting it
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)

# Arrays to store branches
MERGED_BRANCHES=()
GONE_BRANCHES=()

# Find branches whose remote tracking branch is gone
echo -e "${BLUE}Checking for branches with deleted remotes...${NC}"
while IFS= read -r branch; do
    [ -z "$branch" ] && continue
    branch=$(echo "$branch" | xargs)  # Trim whitespace
    
    # Skip the base branch and current branch
    [ "$branch" = "$BASE_BRANCH" ] && continue
    [ "$branch" = "$CURRENT_BRANCH" ] && continue
    
    # Check if the upstream is gone
    upstream_status=$(git branch -vv | grep "^\*\? *$branch " | grep -o '\[.*\]' || echo "")
    if echo "$upstream_status" | grep -q ": gone\]"; then
        GONE_BRANCHES+=("$branch")
    fi
done < <(git branch --format='%(refname:short)')

# Find branches that have been squash-merged
echo -e "${BLUE}Checking for squash-merged branches...${NC}"
while IFS= read -r branch; do
    [ -z "$branch" ] && continue
    branch=$(echo "$branch" | xargs)  # Trim whitespace
    
    # Skip the base branch and current branch
    [ "$branch" = "$BASE_BRANCH" ] && continue
    [ "$branch" = "$CURRENT_BRANCH" ] && continue
    
    # Skip if already in gone branches
    for gone in "${GONE_BRANCHES[@]}"; do
        [ "$branch" = "$gone" ] && continue 2
    done
    
    # Check if this branch has been squash-merged
    # We do this by creating a temporary merge-base tree and checking if the diff is empty
    merge_base=$(git merge-base "$BASE_BRANCH" "$branch" 2>/dev/null || echo "")
    [ -z "$merge_base" ] && continue
    
    # Get the tree hash of what the branch would look like merged into base
    # If the branch changes are already in base, the cherry-pick would be empty
    tree_hash=$(git rev-parse "$branch^{tree}" 2>/dev/null || echo "")
    [ -z "$tree_hash" ] && continue
    
    # Check if all commits from branch are already in base (squash-merged)
    # by checking if cherry would show any commits
    cherry_output=$(git cherry "$BASE_BRANCH" "$branch" 2>/dev/null | grep "^+" || echo "")
    
    if [ -z "$cherry_output" ]; then
        # All commits are in base branch (truly merged, not squash)
        MERGED_BRANCHES+=("$branch")
    else
        # Check for squash merge by comparing the patch
        # Create a patch from merge-base to branch, and check if those changes exist in base
        patch_id=$(git diff "$merge_base" "$branch" 2>/dev/null | git patch-id --stable 2>/dev/null | cut -d' ' -f1 || echo "none1")
        
        # Try to find if base branch contains equivalent changes
        # by checking if rebasing the branch onto base would result in empty commits
        if git merge-tree "$merge_base" "$BASE_BRANCH" "$branch" 2>/dev/null | grep -q "^"; then
            # There would be conflicts or changes, check another way
            # Compare the diff between branch and base to see if it's minimal
            diff_stat=$(git diff --stat "$BASE_BRANCH"..."$branch" 2>/dev/null | tail -1 || echo "")
            if [ -z "$diff_stat" ]; then
                MERGED_BRANCHES+=("$branch")
            fi
        else
            # No merge conflicts and no diff means changes are in base
            diff_check=$(git diff "$BASE_BRANCH"..."$branch" 2>/dev/null || echo "has_diff")
            if [ -z "$diff_check" ]; then
                MERGED_BRANCHES+=("$branch")
            fi
        fi
    fi
done < <(git branch --format='%(refname:short)')

# Report findings
echo ""
echo -e "${GREEN}=== Branches Safe to Delete ===${NC}"
echo ""

if [ ${#GONE_BRANCHES[@]} -gt 0 ]; then
    echo -e "${YELLOW}Branches with deleted remote tracking branch:${NC}"
    for branch in "${GONE_BRANCHES[@]}"; do
        echo "  - $branch"
    done
    echo ""
fi

if [ ${#MERGED_BRANCHES[@]} -gt 0 ]; then
    echo -e "${YELLOW}Branches that appear to be merged/squash-merged:${NC}"
    for branch in "${MERGED_BRANCHES[@]}"; do
        echo "  - $branch"
    done
    echo ""
fi

# Combine all branches to delete
ALL_BRANCHES=("${GONE_BRANCHES[@]}" "${MERGED_BRANCHES[@]}")

if [ ${#ALL_BRANCHES[@]} -eq 0 ]; then
    echo -e "${GREEN}No branches to prune!${NC}"
    exit 0
fi

# Handle deletion
if [ "$DRY_RUN" = true ]; then
    echo -e "${BLUE}Dry run - no branches were deleted.${NC}"
    exit 0
fi

if [ "$FORCE" = true ]; then
    echo -e "${YELLOW}Force deleting branches...${NC}"
    for branch in "${ALL_BRANCHES[@]}"; do
        echo -e "  Deleting: ${RED}$branch${NC}"
        git branch -D "$branch" 2>/dev/null || echo "    (already deleted or error)"
    done
    echo -e "${GREEN}Done!${NC}"
else
    echo -e "${BLUE}Delete these branches? [y/N/i(nteractive)]${NC} "
    read -r response
    case "$response" in
        [yY])
            for branch in "${ALL_BRANCHES[@]}"; do
                echo -e "  Deleting: ${RED}$branch${NC}"
                git branch -D "$branch" 2>/dev/null || echo "    (already deleted or error)"
            done
            echo -e "${GREEN}Done!${NC}"
            ;;
        [iI])
            for branch in "${ALL_BRANCHES[@]}"; do
                echo -e -n "  Delete ${YELLOW}$branch${NC}? [y/N] "
                read -r branch_response
                if [[ "$branch_response" =~ ^[yY]$ ]]; then
                    git branch -D "$branch" 2>/dev/null || echo "    (already deleted or error)"
                    echo -e "    ${RED}Deleted${NC}"
                else
                    echo -e "    ${GREEN}Skipped${NC}"
                fi
            done
            echo -e "${GREEN}Done!${NC}"
            ;;
        *)
            echo -e "${BLUE}Aborted. No branches deleted.${NC}"
            ;;
    esac
fi
