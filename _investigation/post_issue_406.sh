#!/usr/bin/env bash
set -euo pipefail

gh issue comment 406 \
  --repo echotools/nakama \
  --body-file /home/metis/src/nakama/_investigation/echotools_nakama_406_comment.md
