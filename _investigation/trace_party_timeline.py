#!/usr/bin/env python3

import argparse
import json
import os
import sys
from datetime import datetime, timedelta, timezone


DEFAULT_TERMS = (
    "LobbyFindSessionRequest",
    "LobbyMatchmakerStatusRequest",
    "Loaded join directive",
    "Next match not found",
    "Deleted join directive",
    "Joined party group",
    "Party is ready",
    "Waiting for party members to start matchmaking",
    "Matchmaking ticket added",
    "Player joining the match.",
    "Player leaving the match.",
    "Joined entrant.",
    "Leader is currently matchmaking, falling through",
    "Polling to follow party leader",
    "Joining leader's lobby",
    "Joining leader's lobby during poll",
    "Leader's non-social match is persistently non-joinable, releasing follower",
    "Follower cannot join leader's match, redirecting to social lobby",
    "Context canceled but follower is in leader's match (placed by matchmaker)",
    "LobbySessionSuccessv5",
    "LobbyPlayerSessionsRequest",
)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Trace party/match transition logs for specific users.")
    parser.add_argument("--log", default="/var/tmp/nakama-logs/nakama.log", help="Path to nakama.log")
    parser.add_argument("--hours", type=float, default=24, help="Lookback window in hours")
    parser.add_argument("--uid", action="append", default=[], help="User ID to match (repeatable)")
    parser.add_argument("--username", action="append", default=[], help="Username to match exactly (repeatable)")
    parser.add_argument(
        "--term",
        action="append",
        default=[],
        help="Additional substring to match against msg/request_type/request/message",
    )
    return parser.parse_args()


def parse_ts(value: str) -> datetime:
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def main() -> int:
    args = parse_args()
    cutoff = datetime.now(timezone.utc) - timedelta(hours=args.hours)
    usernames = {value.lower() for value in args.username}
    uids = set(args.uid)
    terms = DEFAULT_TERMS + tuple(args.term)

    with open(args.log, "r", encoding="utf-8") as handle:
        for line_number, line in enumerate(handle, 1):
            try:
                entry = json.loads(line)
            except json.JSONDecodeError:
                continue

            ts = entry.get("ts")
            if not ts or parse_ts(ts) < cutoff:
                continue

            uid = entry.get("uid", "")
            username = entry.get("username", "").lower()
            if uids and uid not in uids and username not in usernames:
                continue
            if not uids and usernames and username not in usernames:
                continue

            haystack = " ".join(str(entry.get(key, "")) for key in ("msg", "request_type", "request", "message"))
            if terms and not any(term in haystack for term in terms):
                continue

            try:
                print(f"{line_number}: {line.rstrip()}")
            except BrokenPipeError:
                devnull = os.open(os.devnull, os.O_WRONLY)
                os.dup2(devnull, sys.stdout.fileno())
                return 0

    return 0


if __name__ == "__main__":
    sys.exit(main())
