#!/usr/bin/env bash
# Webhook commands for v3.27.2-evr.299
# DO NOT EXECUTE — prepared for review only.
# Requires .env at project root with WEBHOOK_* variables.
#
# Alternatively, use: ./docs/post-release-notes.sh <channel#> <file>

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
RELEASE_DIR="$SCRIPT_DIR"

# Load .env
if [[ -f "$PROJECT_ROOT/.env" ]]; then
  set -a
  source "$PROJECT_ROOT/.env"
  set +a
else
  echo "Error: .env not found at $PROJECT_ROOT/.env" >&2
  echo "Create .env with WEBHOOK_* variables (see docs/CHANGELOG_PROMPT.md)" >&2
  exit 1
fi

# --- 1. Players (#release-notes) --- HAS CONTENT
echo "Posting 01_players.md to #release-notes..."
"$PROJECT_ROOT/docs/post-release-notes.sh" 1 "$RELEASE_DIR/01_players.md"

# --- 2. EchoTools Core Developers (#echotools-releases) --- HAS CONTENT
echo "Posting 02_echotools.md to #echotools-releases..."
"$PROJECT_ROOT/docs/post-release-notes.sh" 2 "$RELEASE_DIR/02_echotools.md"

# --- 3. App Developers (#app-dev-releases) --- NO CHANGES (skip recommended)
# echo "Posting 03_appdev.md to #app-dev-releases..."
# "$PROJECT_ROOT/docs/post-release-notes.sh" 3 "$RELEASE_DIR/03_appdev.md"

# --- 4. Operators (#operator-releases) --- HAS CONTENT
echo "Posting 04_operators.md to #operator-releases..."
"$PROJECT_ROOT/docs/post-release-notes.sh" 4 "$RELEASE_DIR/04_operators.md"

# --- 5. Competitive (#competitive-releases) --- NO CHANGES (skip recommended)
# echo "Posting 05_competitive.md to #competitive-releases..."
# "$PROJECT_ROOT/docs/post-release-notes.sh" 5 "$RELEASE_DIR/05_competitive.md"

# --- 6. Enforcers (#enforcer-updates) --- NO CHANGES (skip recommended)
# echo "Posting 06_enforcers.md to #enforcer-updates..."
# "$PROJECT_ROOT/docs/post-release-notes.sh" 6 "$RELEASE_DIR/06_enforcers.md"

# --- 7. Full Changelog (#full-changelog) --- HAS CONTENT
echo "Posting 07_changelog.md to #full-changelog..."
"$PROJECT_ROOT/docs/post-release-notes.sh" 7 "$RELEASE_DIR/07_changelog.md"

echo "Done. Announcement (00_announcement.md) must be posted manually to general announcements."
