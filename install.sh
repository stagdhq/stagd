#!/usr/bin/env bash
# Installs the stagd CLI from its GitHub releases:
#
#   curl -fsSL https://stagd.dev/install.sh | bash
#
# STAGD_VERSION picks a release, e.g. v0.3.0 (default: the latest), and
# STAGD_INSTALL_DIR where it goes (default: ~/.local/bin). It checks the
# download against the release's checksums before installing. macOS and
# Linux, on amd64 and arm64.
#
# Everything is in main, called on the last line: a download cut short runs
# nothing.

set -euo pipefail

# The download's folder, removed on exit however it ends.
tmp=""
trap 'if [ -n "$tmp" ]; then rm -rf "$tmp"; fi' EXIT

say() { printf '%s\n' "$*" >&2; }
fail() { say "stagd install: $*"; exit 1; }

main() {
  local repo="stagdhq/stagd"
  local releases="${STAGD_RELEASES_URL:-https://github.com/$repo/releases}"
  local version="${STAGD_VERSION:-latest}"
  local dir="${STAGD_INSTALL_DIR:-$HOME/.local/bin}"

  command -v curl >/dev/null || fail "needs curl"
  command -v tar >/dev/null || fail "needs tar"

  local os arch
  case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) fail "no build for $(uname -s): macOS and Linux only; or: go install github.com/$repo/cmd/stagd@latest" ;;
  esac

  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) fail "no build for $(uname -m): amd64 and arm64 only" ;;
  esac

  local base
  if [ "$version" = latest ]; then
    base="$releases/latest/download"
  else
    base="$releases/download/$version"
  fi

  local asset="stagd_${os}_${arch}.tar.gz"
  tmp="$(mktemp -d)"

  say "Downloading stagd ($version, $os/$arch)…"
  curl -fsSL "$base/$asset" -o "$tmp/$asset" || fail "couldn't download $base/$asset"
  curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || fail "couldn't download $base/checksums.txt"

  # The archive's checksum, from the release's list.
  local want got
  want="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$tmp/checksums.txt")"
  [ -n "$want" ] || fail "$asset isn't in the release's checksums"

  if command -v sha256sum >/dev/null; then
    got="$(sha256sum "$tmp/$asset" | awk '{ print $1 }')"
  else
    got="$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')"
  fi

  [ "$got" = "$want" ] || fail "the download doesn't match its checksum: not installing it"

  tar -xzf "$tmp/$asset" -C "$tmp" stagd
  mkdir -p "$dir"
  install -m 0755 "$tmp/stagd" "$dir/stagd"

  say "Installed $("$dir/stagd" --version 2>/dev/null || echo stagd) to $dir/stagd"

  case ":$PATH:" in
    *":$dir:"*) say "Next: stagd login" ;;
    *)
      say ""
      say "$dir isn't on your PATH. Add it, e.g. in ~/.zshrc or ~/.bashrc:"
      say ""
      say "  export PATH=\"$dir:\$PATH\""
      say ""
      say "then: stagd login"
      ;;
  esac
}

main "$@"
