#!/bin/bash
#
# find-unbalanced-matches.sh
# 
# Polls https://g.echovrce.com/status/matches and finds Echo Arena matches
# where a single team has more than 4 players. Outputs the match label (id).
#
# Usage: ./scripts/find-unbalanced-matches.sh [poll_interval_seconds]
#

set -euo pipefail

# Configuration
API_URL="https://g.echovrce.com/status/matches"
POLL_INTERVAL="${1:-5}"  # Default to 5 seconds if not provided

# Check for required commands
if ! command -v curl &> /dev/null; then
    echo "Error: curl is required but not installed." >&2
    exit 1
fi

if ! command -v jq &> /dev/null; then
    echo "Error: jq is required but not installed." >&2
    exit 1
fi

echo "Polling ${API_URL} every ${POLL_INTERVAL} seconds..."
echo "Looking for Echo Arena matches with more than 4 players on a single team..."
echo ""

while true; do
    # Fetch the match data
    response=$(curl -s "${API_URL}")
    
    if [ $? -ne 0 ] || [ -z "$response" ]; then
        echo "[$(date -u +"%Y-%m-%d %H:%M:%S UTC")] Error: Failed to fetch data from API" >&2
        sleep "${POLL_INTERVAL}"
        continue
    fi
    
    # Process each match label
    # Filter for echo_arena mode matches, then check team sizes
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
        | "\($match.id) - Blue: \($blue_count) Orange: \($orange_count)"
    ' | while IFS= read -r line; do
        if [ -n "$line" ]; then
            echo "[$(date -u +"%Y-%m-%d %H:%M:%S UTC")] Found unbalanced match: $line"
        fi
    done
    
    sleep "${POLL_INTERVAL}"
done
