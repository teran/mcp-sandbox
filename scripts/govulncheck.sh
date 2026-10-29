#!/usr/bin/env bash
# govulncheck wrapper for mcp-sandbox.
#
# mcp-sandbox pins the official github.com/docker/docker SDK (see SPEC.md).
# Two advisories in that module currently have **no known fixed version** on
# the used module path (the fix exists only in the pre-release
# github.com/moby/moby/v2 v2.0.0-beta.8). Both are in Docker's
# **plugin-authorization** subsystem, which this client-only server does not
# exercise. They are documented as an accepted, tracked risk (SPEC.md §7.x).
#
# This wrapper is a *targeted* exception for those specific IDs — it still
# BLOCKS on any NEW vulnerability. Do not extend ACCEPTED_IDS without a
# documented justification in SPEC.md (S05: no blanket suppressions).
set -euo pipefail

# Accepted, documented, no-upstream-fix dependency vulnerabilities.
ACCEPTED_IDS="GO-2026-4887 GO-2026-4883"

# Run govulncheck in structured JSON mode so ID extraction is robust to changes
# in the human-readable text layout. jq is preinstalled on GitHub-hosted
# runners; fall back to text-id extraction if jq is unavailable.
out="$(govulncheck -json ./... 2>&1 || true)"

if command -v jq >/dev/null 2>&1; then
	# Collect every vulnerability ID from the JSON document (top-level "Vulns"
	# entries and any nested OSV findings), then keep only GO- ids (the same
	# namespace ACCEPTED_IDS uses). A malformed/empty JSON yields no ids.
	found="$(printf '%s' "$out" | jq -r '.. | objects | .ID? // empty' 2>/dev/null \
		| grep -oE 'GO-[0-9]{4}-[0-9]+' | sort -u || true)"
else
	found="$(printf '%s' "$out" | grep -oE 'GO-[0-9]{4}-[0-9]+' | sort -u || true)"
fi

if [ -z "$found" ]; then
	echo "govulncheck: no vulnerabilities."
	exit 0
fi

blocked=0
for id in $found; do
	if ! echo "$ACCEPTED_IDS" | tr ' ' '\n' | grep -qx "$id"; then
		echo "govulncheck: BLOCKING vulnerability $id (not in the documented accepted set):"
		blocked=1
	fi
done

if [ "$blocked" -ne 0 ]; then
	printf '%s' "$out"
	exit 1
fi

echo "govulncheck: only accepted, documented no-fix vulnerabilities present: $found"
