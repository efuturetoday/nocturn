#!/bin/sh
# Refuses a commit that changes only code, until the documentation question has been answered.
#
# Exit 0: nothing staged under internal/ or cmd/, docs are staged too, or this exact file set was
# already presented once.
# Exit 1: the reason is on stdout.
set -eu

files=$(git diff --cached --name-only --diff-filter=ACMRD 2>/dev/null || true)
src=$(printf '%s\n' "$files" | grep -E '^(internal|cmd)/' || true)
docs=$(printf '%s\n' "$files" | grep -E '^docs/' || true)
[ -n "$src" ] || exit 0
[ -z "$docs" ] || exit 0

stamp="$(git rev-parse --git-dir)/docs-gate-stamp"
hash=$(printf '%s' "$files" | shasum | cut -d' ' -f1)
if [ -f "$stamp" ] && [ "$(cat "$stamp")" = "$hash" ]; then
	exit 0
fi
printf '%s' "$hash" >"$stamp"

cat <<EOF
This commit changes code and no documentation:
$src

Check whether ANY documentation needs updating — NOT just the tool/capability reference. The areas
of the docs site (docs/src/content/docs/): Guides (guides/), Architecture & Concepts (architecture/),
Reference incl. Sandbox/Runtime (reference/), plus the tool and capability data
(docs/src/data/tools/, docs/src/data/capabilities/, rules in docs/CONVENTIONS.md) and the sidebar
(docs/astro.config.mjs). Agent-facing knowledge lives in .agents/docs/ and ADRS.md and counts too.

Document ONLY the concrete changes of this commit, OR briefly justify why none are needed for
exactly these changes. Then run git commit again — the same staged content is not blocked twice.
EOF
exit 1
