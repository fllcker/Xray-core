#!/usr/bin/env bash
#
# Verifies that every patch documented in PATCHES.md is still present in the
# tree.
#
# A rebase onto a new upstream release can drop one of our lines without
# breaking the build: the code still compiles, the tests still pass, and the
# feature is silently gone. This is what catches that.
#
# The list of patch ids comes from PATCHES.md itself, so the two cannot drift
# apart. FLK-000 is skipped: it documents an invariant, not a code site.

set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

if [ ! -f PATCHES.md ]; then
	echo "PATCHES.md not found" >&2
	exit 1
fi

ids=$(grep -oE '^## FLK-[0-9]+' PATCHES.md | awk '{print $2}' | grep -v '^FLK-000$')

if [ -z "$ids" ]; then
	echo "no patch ids found in PATCHES.md" >&2
	exit 1
fi

status=0
for id in $ids; do
	# Only code counts. A mention in PATCHES.md is documentation, not a patch.
	count=$(grep -rn --include='*.go' "\[fllcker:$id\]" . | wc -l | tr -d ' ')
	if [ "$count" -eq 0 ]; then
		echo "MISSING  $id  documented in PATCHES.md, absent from the tree"
		status=1
	else
		echo "ok       $id  $count site(s)"
	fi
done

if [ "$status" -ne 0 ]; then
	echo
	echo "A documented patch is missing. Either it was lost in a rebase and must"
	echo "be restored from its card in PATCHES.md, or it was removed on purpose"
	echo "and its card should go with it."
fi

exit $status
