#!/bin/bash
#
# Prune remote git branches on origin that have been merged, cherry-picked,
# or squash-merged into the default branch.
#
# Usage:
#   ./prune-remote-branches.sh             # Interactive mode - prompts before deleting
#   ./prune-remote-branches.sh -f          # Force mode - deletes without prompting
#   ./prune-remote-branches.sh -n          # Dry run - only shows what would be deleted
#   ./prune-remote-branches.sh -b main     # Specify a different base branch
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
REMOTE="origin"

usage() {
    echo "Usage: $0 [-f] [-n] [-b base_branch]"
    echo "  -f  Force delete without prompting"
    echo "  -n  Dry run (show what would be deleted)"
    echo "  -b  Specify base branch (default: auto-detect from origin HEAD)"
}

# Parse arguments
while getopts "fnb:" opt; do
    case $opt in
        f) FORCE=true ;;
        n) DRY_RUN=true ;;
        b) BASE_BRANCH="$OPTARG" ;;
        *)
            usage
            exit 1
            ;;
    esac
done

if [ "$REMOTE" != "origin" ]; then
    echo -e "${RED}Error: Remote must be origin.${NC}"
    exit 1
fi

if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    echo -e "${RED}Error: Not inside a git repository.${NC}"
    exit 1
fi

if ! git remote get-url "$REMOTE" >/dev/null 2>&1; then
    echo -e "${RED}Error: Remote '$REMOTE' does not exist.${NC}"
    exit 1
fi

# Get the default branch if not specified
if [ -z "$BASE_BRANCH" ]; then
    BASE_BRANCH=$(git symbolic-ref "refs/remotes/$REMOTE/HEAD" 2>/dev/null | sed "s@^refs/remotes/$REMOTE/@@" || echo "")

    if [ -z "$BASE_BRANCH" ]; then
        if git show-ref --verify --quiet "refs/remotes/$REMOTE/main"; then
            BASE_BRANCH="main"
        elif git show-ref --verify --quiet "refs/remotes/$REMOTE/master"; then
            BASE_BRANCH="master"
        else
            echo -e "${RED}Error: Could not determine default branch. Use -b to specify.${NC}"
            exit 1
        fi
    fi
fi

echo -e "${BLUE}Remote: ${REMOTE}${NC}"
echo -e "${BLUE}Base branch: ${BASE_BRANCH}${NC}"
echo ""

# Fetch latest from remote and prune remote tracking branches
echo -e "${BLUE}Fetching latest from ${REMOTE}...${NC}"
git fetch "$REMOTE" --prune

declare -A BASE_PATCH_IDS

commit_patch_id() {
    local commit_sha="$1"
    git show --no-color --pretty=format: "$commit_sha" 2>/dev/null | git patch-id --stable 2>/dev/null | awk '{print $1}' || true
}

diff_patch_id() {
    git diff --no-color "$1" "$2" 2>/dev/null | git patch-id --stable 2>/dev/null | awk '{print $1}' || true
}

while IFS= read -r commit_sha; do
    patch_id=$(commit_patch_id "$commit_sha")
    [ -n "$patch_id" ] && BASE_PATCH_IDS["$patch_id"]=1
done < <(git rev-list --no-merges "$REMOTE/$BASE_BRANCH")

MERGED_REMOTE_BRANCHES=()

echo -e "${BLUE}Checking for remote branches already in ${REMOTE}/${BASE_BRANCH}...${NC}"
while IFS= read -r remote_ref; do
    [ -z "$remote_ref" ] && continue

    branch=${remote_ref#refs/remotes/$REMOTE/}

    # Skip remote HEAD and base branch
    [ "$branch" = "HEAD" ] && continue
    [ "$branch" = "$BASE_BRANCH" ] && continue

    remote_branch="$REMOTE/$branch"

    if git merge-base --is-ancestor "$remote_branch" "$REMOTE/$BASE_BRANCH"; then
        MERGED_REMOTE_BRANCHES+=("$branch")
        continue
    fi

    merge_base=$(git merge-base "$REMOTE/$BASE_BRANCH" "$remote_branch" 2>/dev/null || echo "")
    [ -z "$merge_base" ] && continue

    if git diff --quiet "$REMOTE/$BASE_BRANCH".."$remote_branch" --; then
        MERGED_REMOTE_BRANCHES+=("$branch")
        continue
    fi

    all_replayed=true
    while IFS= read -r commit_sha; do
        [ -z "$commit_sha" ] && continue
        patch_id=$(commit_patch_id "$commit_sha")
        if [ -z "$patch_id" ] || [ -z "${BASE_PATCH_IDS[$patch_id]}" ]; then
            all_replayed=false
            break
        fi
    done < <(git rev-list --no-merges "$REMOTE/$BASE_BRANCH..$remote_branch")

    if [ "$all_replayed" = true ]; then
        MERGED_REMOTE_BRANCHES+=("$branch")
        continue
    fi

    net_patch_id=$(diff_patch_id "$REMOTE/$BASE_BRANCH" "$remote_branch")
    if [ -n "$net_patch_id" ] && [ -n "${BASE_PATCH_IDS[$net_patch_id]}" ]; then
        MERGED_REMOTE_BRANCHES+=("$branch")
        continue
    fi

done < <(git for-each-ref --format='%(refname)' "refs/remotes/$REMOTE")

echo ""
echo -e "${GREEN}=== Remote branches safe to delete from ${REMOTE} ===${NC}"
echo ""

if [ ${#MERGED_REMOTE_BRANCHES[@]} -gt 0 ]; then
    for branch in "${MERGED_REMOTE_BRANCHES[@]}"; do
        echo "  - $branch"
    done
    echo ""
else
    echo -e "${GREEN}No remote branches to prune.${NC}"
    exit 0
fi

if [ "$DRY_RUN" = true ]; then
    echo -e "${BLUE}Dry run - no remote branches were deleted.${NC}"
    exit 0
fi

delete_branch() {
    local branch="$1"
    echo -e "  Deleting from ${REMOTE}: ${RED}${branch}${NC}"
    git push "$REMOTE" --delete "$branch" 2>/dev/null || echo "    (already deleted or error)"
}

if [ "$FORCE" = true ]; then
    echo -e "${YELLOW}Force deleting remote branches...${NC}"
    for branch in "${MERGED_REMOTE_BRANCHES[@]}"; do
        delete_branch "$branch"
    done
    echo -e "${GREEN}Done!${NC}"
    exit 0
fi

echo -e "${BLUE}Delete these branches from ${REMOTE}? [y/N/i(nteractive)]${NC} "
read -r response
case "$response" in
    [yY])
        for branch in "${MERGED_REMOTE_BRANCHES[@]}"; do
            delete_branch "$branch"
        done
        echo -e "${GREEN}Done!${NC}"
        ;;
    [iI])
        for branch in "${MERGED_REMOTE_BRANCHES[@]}"; do
            echo -e -n "  Delete ${YELLOW}${branch}${NC} from ${REMOTE}? [y/N] "
            read -r branch_response
            if [[ "$branch_response" =~ ^[yY]$ ]]; then
                delete_branch "$branch"
                echo -e "    ${RED}Deleted${NC}"
            else
                echo -e "    ${GREEN}Skipped${NC}"
            fi
        done
        echo -e "${GREEN}Done!${NC}"
        ;;
    *)
        echo -e "${BLUE}Aborted. No remote branches deleted.${NC}"
        ;;
esac