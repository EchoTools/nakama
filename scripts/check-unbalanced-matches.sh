#!/bin/bash
#
# check-unbalanced-matches.sh
#
# Single check of https://g.echovrce.com/status/matches for Echo Arena matches
# where a single team has more than 4 players. Outputs the match label (id).
#
# Usage: ./scripts/check-unbalanced-matches.sh
#

set -euo pipefail

API_URL="https://g.echovrce.com/status/matches"

if ! command -v curl &> /dev/null; then
    echo "Error: curl is required but not installed." >&2
    exit 1
fi

if ! command -v jq &> /dev/null; then
    echo "Error: jq is required but not installed." >&2
    exit 1
fi

response=$(curl -s "${API_URL}")

if [ $? -ne 0 ] || [ -z "$response" ]; then
    echo "Error: Failed to fetch data from API" >&2
    exit 1
fi

found_any=false

echo "$response" | jq -r '
    .labels[]?
    | select(.mode == "echo_arena")
    | . as $match
    | (
        [.players[]? | select(.team == "blue")] | length
    ) as $blue_count
    | (
        [.players[]? | select(.team == "orange")] | length
    ) as $orange_count
    | select($blue_count > 4 or $orange_count > 4)
    | "\($match)"
' | while IFS= read -r line; do
    if [ -n "$line" ]; then
        echo "$line"
        found_any=true
    fi
done

if [ "$found_any" = false ]; then
    exit 0
fi
