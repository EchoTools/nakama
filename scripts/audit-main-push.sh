#!/usr/bin/env bash
#
# Report any commit that reached main without an associated pull request.
#
# THIS IS DETECTION, NOT PREVENTION. It runs after the push has already landed.
# By the time it goes red, the commit is on main and anyone who fetched has it.
# It exists to make a direct push loud and dated rather than silent, and to give
# the audit trail a red mark to point at -- not to stop one.
#
# The mechanism that would actually PREVENT this is GitHub branch protection
# requiring a pull request to land on main. That is a repository setting, not a
# file, and it constrains every human with push access too, so it is the
# repository owner's decision and deliberately not made here.
#
# The other layer is .githooks/pre-push, which DOES prevent -- but only in a
# clone that has run `just hooks`. A fresh clone, a new worktree, or an agent
# starting cold is unprotected until that runs, which is exactly the population
# the guard was built for. This check is the backstop for that gap: it is
# server-side, arms itself, and cannot be skipped by a clone that never set
# anything up.
#
# Usage:
#   scripts/audit-main-push.sh <sha> [<sha>...]
#
# Requires gh with repo read access. GH_TOKEN is provided in Actions.

set -uo pipefail

if [ "$#" -eq 0 ]; then
	echo "usage: $0 <sha> [<sha>...]" >&2
	exit 2
fi

repo="${GITHUB_REPOSITORY:-EchoTools/nakama}"
violations=0

for sha in "$@"; do
	# A commit that arrived through a PR -- the merge commit itself, and every
	# commit the PR carried -- is associated with that PR. A commit pushed
	# straight to main is associated with none.
	#
	# On API failure this prints nothing and count stays empty; treat that as
	# UNKNOWN and do not fail the build on it. A flaky API call must not read as
	# a policy violation, or the check trains people to ignore it.
	count="$(gh api "repos/${repo}/commits/${sha}/pulls" --jq 'length' 2>/dev/null)"

	if [ -z "$count" ]; then
		echo "  ?  ${sha}  could not query associated PRs (API error) -- not counted" >&2
		continue
	fi

	if [ "$count" -eq 0 ]; then
		subject="$(git log -1 --format='%s' "$sha" 2>/dev/null || echo '<unknown>')"
		author="$(git log -1 --format='%an <%ae>' "$sha" 2>/dev/null || echo '<unknown>')"
		echo "  ✗  ${sha}  ${subject}"
		echo "         author: ${author}"
		violations=$((violations + 1))
	else
		echo "  ✓  ${sha}  (PR #$(gh api "repos/${repo}/commits/${sha}/pulls" --jq '.[0].number' 2>/dev/null))"
	fi
done

if [ "$violations" -gt 0 ]; then
	cat >&2 <<EOF

${violations} commit(s) reached main without a pull request.

This check is post-hoc: the commit is already on main and this cannot undo it.
What it gives you is a dated, red record instead of a silent one.

If this was accidental -- the usual cause is a push resolving to main via a
worktree's upstream, see .githooks/pre-push -- the commit stays. Rewriting
shared history to hide the mistake is worse than the mistake.

If it was intentional, nothing here objects to that; it just refuses to let it
be quiet.
EOF
	exit 1
fi

echo "every commit in this push is associated with a pull request"
