#!/usr/bin/env python3

import argparse
import json
import os
import sys
from datetime import datetime


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Extract evidence lines for specific match IDs and users.")
    parser.add_argument("--log", default="/var/tmp/nakama-logs/nakama.log", help="Path to nakama.log")
    parser.add_argument("--start", required=True, help="Inclusive ISO-8601 start time")
    parser.add_argument("--end", required=True, help="Inclusive ISO-8601 end time")
    parser.add_argument("--mid", action="append", default=[], help="Match ID substring to match (repeatable)")
    parser.add_argument("--uid", action="append", default=[], help="User ID to match (repeatable)")
    parser.add_argument("--username", action="append", default=[], help="Username to match exactly (repeatable)")
    return parser.parse_args()


def parse_ts(value: str) -> datetime:
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def main() -> int:
    args = parse_args()
    start = parse_ts(args.start)
    end = parse_ts(args.end)
    mids = tuple(args.mid)
    uids = set(args.uid)
    usernames = {value.lower() for value in args.username}

    with open(args.log, "r", encoding="utf-8") as handle:
        for line_number, line in enumerate(handle, 1):
            try:
                entry = json.loads(line)
            except json.JSONDecodeError:
                continue

            ts = entry.get("ts")
            if not ts:
                continue

            parsed_ts = parse_ts(ts)
            if parsed_ts < start or parsed_ts > end:
                continue

            line_text = line.rstrip()
            uid = entry.get("uid", "")
            username = entry.get("username", "").lower()

            if mids and not any(mid in line_text for mid in mids):
                if uids and uid not in uids and username not in usernames:
                    continue
                if not uids and usernames and username not in usernames:
                    continue

            try:
                print(f"{line_number}: {line_text}")
            except BrokenPipeError:
                devnull = os.open(os.devnull, os.O_WRONLY)
                os.dup2(devnull, sys.stdout.fileno())
                return 0

    return 0


if __name__ == "__main__":
    sys.exit(main())
