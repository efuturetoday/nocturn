#!/bin/sh
# Refuses staged Go changes that have not been through the Go review skills.
#
# Exit 0: nothing staged in Go, or this exact diff was already presented once.
# Exit 1: the reason is on stdout. The same staged content is never blocked twice — the caller is
# expected to review, fix or justify, and then run the commit again.
#
# Called by .githooks/pre-commit (every agent, and humans) and by .claude/settings.json (Claude Code,
# which shows the reason before git runs at all).
set -eu

gofiles=$(git diff --cached --name-only --diff-filter=ACMR -- '*.go' 2>/dev/null || true)
[ -n "$gofiles" ] || exit 0

stamp="$(git rev-parse --git-dir)/go-review-stamp"
hash=$(git diff --cached -- '*.go' | shasum | cut -d' ' -f1)
if [ -f "$stamp" ] && [ "$(cat "$stamp")" = "$hash" ]; then
	exit 0
fi
printf '%s' "$hash" >"$stamp"

cat <<EOF
These staged Go files have NOT been reviewed yet:
$gofiles

Review them with the installed Go skills BEFORE committing:
1. Invoke the orchestrator skill cc-skills-golang:golang-how-to. It is the one that KNOWS which
   skills exist and which match this diff — do not guess a skill name and do not substitute one that
   is not in its list.
2. Actually LOAD what it routes you to, not just the orchestrator. A commit is always a style
   review, so its review row (cc-skills-golang:golang-code-style, cc-skills-golang:golang-naming,
   cc-skills-golang:golang-lint) applies on top of whatever the subject matter of the diff adds.
3. If a skill it names is not installed, say so in your reply rather than quietly reviewing without
   it.

Fix the findings, or briefly justify each one you leave as is. Then run git commit again — the same
staged content is not blocked twice.
EOF
exit 1
