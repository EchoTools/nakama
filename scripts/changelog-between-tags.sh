#!/bin/bash
# Generate changelog between two EVR tags
# Usage: ./changelog-between-tags.sh <from_version> [to_version]
# Example: ./changelog-between-tags.sh 238 240
# If to_version omitted, assumes from_version+1

set -euo pipefail

if [ $# -lt 1 ] || [ $# -gt 2 ]; then
    echo "Usage: $0 <from_version> [to_version]"
    echo "Example: $0 238 240"
    echo "If to_version omitted, assumes from_version+1"
    exit 1
fi

FROM="$1"
TO="${2:-$((FROM+1))}"

FROMVER="v3.27.2-evr.$FROM"
TOVER="v3.27.2-evr.$TO"

HEADER="Changes FROM $FROMVER (exclusive) UP TO $TOVER (inclusive):"

OUTPUT=""
for ((i=FROM+1; i<=TO; i++)); do
    TAG="v3.27.2-evr.$i"
    PREV_TAG="v3.27.2-evr.$((i-1))"
    
    COMMITS=$(git log "$PREV_TAG..$TAG" --pretty=format:"---%nCommit: %h%nSubject: %s%nBody:%n%b" | \
        grep -iv -e "Signed-off-by" -e "Co-Authored-by")
    
    if [ -n "$COMMITS" ]; then
        OUTPUT+="
## $TAG

$COMMITS
"
    fi
done

FULL_OUTPUT="$HEADER
$OUTPUT"

echo "$FULL_OUTPUT" | wl-copy
echo "$FULL_OUTPUT"
