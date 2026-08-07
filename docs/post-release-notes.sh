#!/usr/bin/env bash
set -euo pipefail

# Post release notes to Discord via webhook.
# Usage: ./docs/post-release-notes.sh <channel_number> <file>
#   channel_number: 1-7 (see CHANGELOG_PROMPT.md for mapping)
#   file: path to a file containing the message content (plain text/Discord markdown)
#
# Webhook URLs are read from .env in the project root.
# Messages exceeding Discord's 2000 character limit are split automatically.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# Load .env
if [[ -f "$PROJECT_ROOT/.env" ]]; then
  set -a
  source "$PROJECT_ROOT/.env"
  set +a
else
  echo "Error: .env not found at $PROJECT_ROOT/.env" >&2
  exit 1
fi

declare -A WEBHOOKS=(
  [1]="${WEBHOOK_RELEASE_NOTES:?Missing WEBHOOK_RELEASE_NOTES in .env}"
  [2]="${WEBHOOK_ECHOTOOLS_RELEASES:?Missing WEBHOOK_ECHOTOOLS_RELEASES in .env}"
  [3]="${WEBHOOK_APP_DEV_RELEASES:?Missing WEBHOOK_APP_DEV_RELEASES in .env}"
  [4]="${WEBHOOK_OPERATOR_RELEASES:?Missing WEBHOOK_OPERATOR_RELEASES in .env}"
  [5]="${WEBHOOK_COMPETITIVE_RELEASES:?Missing WEBHOOK_COMPETITIVE_RELEASES in .env}"
  [6]="${WEBHOOK_ENFORCER_UPDATES:?Missing WEBHOOK_ENFORCER_UPDATES in .env}"
  [7]="${WEBHOOK_FULL_CHANGELOG:?Missing WEBHOOK_FULL_CHANGELOG in .env}"
)

declare -A NAMES=(
  [1]="#release-notes (Players)"
  [2]="#echotools-releases (EchoTools devs)"
  [3]="#app-dev-releases (App developers)"
  [4]="#operator-releases (Guild owners/operators/mods)"
  [5]="#competitive-releases (Comp hosts/allocators/coordinators)"
  [6]="#enforcer-updates (In-game mods/enforcers)"
  [7]="#full-changelog (Detailed changelog)"
)

if [[ $# -lt 2 ]]; then
  echo "Usage: $0 <channel_number 1-7> <file>"
  echo ""
  echo "Channels:"
  for i in 1 2 3 4 5 6 7; do
    echo "  $i  ${NAMES[$i]}"
  done
  exit 1
fi

channel="$1"
file="$2"

if [[ -z "${WEBHOOKS[$channel]+x}" ]]; then
  echo "Error: channel must be 1-7" >&2
  exit 1
fi

if [[ ! -f "$file" ]]; then
  echo "Error: file not found: $file" >&2
  exit 1
fi

webhook="${WEBHOOKS[$channel]}"
name="${NAMES[$channel]}"
content=$(<"$file")

if [[ -z "$content" ]]; then
  echo "Error: file is empty" >&2
  exit 1
fi

# Split content into chunks of at most 2000 characters, breaking at newlines.
chunks=()
current=""
while IFS= read -r line || [[ -n "$line" ]]; do
  candidate="$current"
  if [[ -n "$candidate" ]]; then
    candidate+=$'\n'
  fi
  candidate+="$line"

  if [[ ${#candidate} -gt 2000 ]]; then
    if [[ -n "$current" ]]; then
      chunks+=("$current")
    fi
    # If a single line exceeds 2000, force-split it
    while [[ ${#line} -gt 2000 ]]; do
      chunks+=("${line:0:2000}")
      line="${line:2000}"
    done
    current="$line"
  else
    current="$candidate"
  fi
done <<< "$content"
if [[ -n "$current" ]]; then
  chunks+=("$current")
fi

total=${#chunks[@]}
echo "Posting to $name ($total message(s))..."

for i in "${!chunks[@]}"; do
  payload=$(jq -n --arg c "${chunks[$i]}" '{content: $c}')
  response=$(curl -s -w "\n%{http_code}" -H "Content-Type: application/json" -d "$payload" "$webhook")
  http_code=$(echo "$response" | tail -1)
  body=$(echo "$response" | head -n -1)

  if [[ "$http_code" -ge 200 && "$http_code" -lt 300 ]]; then
    echo "  Part $((i + 1))/$total posted (HTTP $http_code)"
  else
    echo "  Error posting part $((i + 1))/$total (HTTP $http_code): $body" >&2
    exit 1
  fi

  # Rate limit: wait between chunks
  if [[ $((i + 1)) -lt $total ]]; then
    sleep 1
  fi
done

echo "Done."
