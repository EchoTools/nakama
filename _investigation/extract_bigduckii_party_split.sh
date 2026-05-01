#!/usr/bin/env bash
set -euo pipefail

LOG_FILE="${1:-/var/tmp/nakama-logs/nakama.log}"

if [[ ! -f "$LOG_FILE" ]]; then
  echo "log file not found: $LOG_FILE" >&2
  exit 1
fi

echo "== Identity =="
rg -n '"username":"bigduckii"|"evrid":"OVR-ORG-18764"|"uid":"ecc390b5-2cc9-48c8-9f4a-876425a494a2"' "$LOG_FILE" | head -n 20
echo

echo "== Matchmaker placement and shared party =="
awk 'NR>=224660 && NR<=224745 {print NR ":" $0}' "$LOG_FILE" \
  | rg 'Joined entrant|Match built|qvinoo\.|bigduckii|party_id|successful|Matchmaking stream closed|Context canceled'
echo

echo "== Split window =="
awk 'NR>=224950 && NR<=225330 {print NR ":" $0}' "$LOG_FILE" \
  | rg 'LobbyPlayerSessionsRequest|Player leaving the match|Load Stats|Voip Loudness|qvinoo\.|bigduckii|1fdccb5d-6df5-4168-bded-8edc855803a4|7a23f4a2-872d-4cc5-9201-7e077f855c00'
echo

echo "== Code: follower success on tracker state =="
nl -ba server/evr_lobby_find.go | sed -n '897,969p'
echo

echo "== Code: service-stream retarget before client completes load =="
nl -ba server/evr_lobby_joinentrant.go | sed -n '173,275p'
