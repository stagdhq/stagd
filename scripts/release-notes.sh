#!/usr/bin/env bash
# A release's notes, from the commits since the previous tag: the tag's own
# message as the summary, then what's new (feat), fixes (fix) and the rest
# worth saying (deploy, docs, perf, refactor); chores are left out. The
# release workflow makes them the GitHub release's notes, which are the
# changelog.
#
#   scripts/release-notes.sh v0.3.0 > notes.md
#
# Commits say what they are the conventional way: "feat(cli): …", "fix: …";
# a "!" before the colon marks a breaking change.
set -euo pipefail

tag="$1"
repo="https://github.com/stagdhq/stagd"
prev="$(git describe --tags --abbrev=0 "$tag^" 2>/dev/null || true)"
range="${prev:+$prev..}$tag"

# The annotated tag's message, without a leading "v1.2.3: ".
summary="$(git tag -l --format='%(contents)' "$tag" | sed -E "1s/^$tag:[[:space:]]*//")"

if [ -n "$summary" ]; then
  printf '%s\n\n' "$summary"
fi

section() {
  local title="$1" types="$2" lines

  lines="$(git log --no-merges --format='%h %s' "$range" | grep -E "^[0-9a-f]+ ($types)(\([^)]*\))?!?: " || true)"
  [ -n "$lines" ] || return 0

  printf '## %s\n\n' "$title"

  while read -r sha subject; do
    local scope text breaking=""

    scope="$(sed -nE 's/^[a-z]+\(([^)]*)\)!?: .*/\1/p' <<<"$subject")"
    text="$(sed -E 's/^[a-z]+(\([^)]*\))?!?: //' <<<"$subject")"

    if grep -qE '^[a-z]+(\([^)]*\))?!: ' <<<"$subject"; then
      breaking="**Breaking:** "
    fi

    printf -- '- %s%s%s ([%s](%s/commit/%s))\n' "$breaking" "${scope:+**$scope:** }" "$text" "$sha" "$repo" "$sha"
  done <<<"$lines"

  echo
}

section "New" "feat"
section "Fixes" "fix"
section "Also" "deploy|docs|perf|refactor"

cat <<MD
## Install or update

\`\`\`bash
curl -fsSL https://stagd.dev/install.sh | bash   # macOS, Linux; or: brew install stagdhq/tap/stagd
irm https://stagd.dev/install.ps1 | iex          # Windows, in PowerShell
stagd update                                      # an install from before; Homebrew: brew upgrade stagdhq/tap/stagd
\`\`\`
MD

if [ -n "$prev" ]; then
  printf '\nAll changes: %s/compare/%s...%s\n' "$repo" "$prev" "$tag"
fi
