#!/usr/bin/env bash
set -euo pipefail

LOG_FILE="${1:-/var/tmp/nakama-logs/nakama.log}"

if [[ ! -f "$LOG_FILE" ]]; then
  echo "log file not found: $LOG_FILE" >&2
  exit 1
fi

usage() {
  cat <<'EOF'
Usage:
  party_split_extract.sh [log_file] user USERNAME
  party_split_extract.sh [log_file] party PARTY_ID_OR_NAME
  party_split_extract.sh [log_file] match MATCH_ID
  party_split_extract.sh [log_file] split USERNAME

Examples:
  party_split_extract.sh user bigduckii
  party_split_extract.sh party duck10
  party_split_extract.sh match 1fdccb5d-6df5-4168-bded-8edc855803a4
  party_split_extract.sh split bigduckii
EOF
}

if [[ $# -lt 2 ]]; then
  usage
  exit 1
fi

mode="$2"
needle="${3:-}"

if [[ -z "$needle" ]]; then
  usage
  exit 1
fi

case "$mode" in
  user)
    rg -n "$needle|partyGroupName|partyID|LobbyFindSessionRequest|Player joining the match\\.|Joined entrant\\.|Created reconnect reservation|Player leaving the match\\." "$LOG_FILE"
    ;;
  party)
    rg -n "$needle|Joined party group|Party is ready|Waiting for party members to start matchmaking|Match built\\.|Player joining the match\\.|Joined entrant\\." "$LOG_FILE"
    ;;
  match)
    rg -n "$needle|Player joining the match\\.|Joined entrant\\.|Player leaving the match\\.|Created reconnect reservation|Loaded join directive|Joining next match" "$LOG_FILE"
    ;;
  split)
    rg -n "$needle|Leader's match is full or closed|persistently non-joinable|Follower cannot join leader's match, redirecting to social lobby|failed to join existing social lobby: failed to get entrant metadata|LobbySessionFailurev4" "$LOG_FILE"
    ;;
  *)
    usage
    exit 1
    ;;
esac
