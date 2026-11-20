#!/bin/sh
# POSIX installer/launcher. release.tsv is trusted, reviewed plugin metadata,
# never a checksum downloaded alongside an untrusted artifact.
# Production releases require GitHub build provenance before any execution.
set -eu
umask 077
fail() { printf '%s\n' "chronicle-mcp: $*" >&2; exit 1; }
action=${1:-}
case "$action" in start|status|stop|headers) ;; *) fail 'usage: chronicle.sh start|status|stop|headers [--force]' ;; esac
shift
case "$action:$*" in start:|status:|stop:|stop:--force|headers:) ;; *) fail 'only stop accepts --force' ;; esac
if [ "$action" = headers ]; then action=auth; set -- headers; fi
case "$(uname -s)" in Linux) os=linux ;; Darwin) os=darwin ;; *) fail 'installer supports Linux and macOS only' ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) fail 'unsupported CPU architecture' ;; esac
if [ -n "${CHRONICLE_DEV_BINARY:-}" ]; then
  case "$CHRONICLE_DEV_BINARY" in /*) ;; *) fail 'CHRONICLE_DEV_BINARY must be an absolute path' ;; esac
  [ -f "$CHRONICLE_DEV_BINARY" ] && [ -x "$CHRONICLE_DEV_BINARY" ] || fail 'development binary is not executable'
  printf '%s\n' 'chronicle-mcp: using explicitly selected development binary; release integrity NOT verified' >&2
  exec "$CHRONICLE_DEV_BINARY" "$action" "$@"
fi
root=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd -P)
read -r version state < "$root/release.tsv"
case "$state" in attested|released) ;; *) fail 'this plugin release is unreleased; no trusted artifacts exist. Build locally and explicitly set CHRONICLE_DEV_BINARY, or install a released plugin.' ;; esac
case "$version" in ''|*[!a-zA-Z0-9._-]*|.|..) fail 'invalid pinned version' ;; esac
if [ "$state" = attested ]; then
  url="https://github.com/Emyrk/chronicle-mcp/releases/download/$version/chronicle-mcp-$os-$arch"
  command -v gh >/dev/null 2>&1 || fail 'GitHub CLI (gh) is required for release verification. Ask the user for consent to install it from a trusted package manager; never bypass verification.'
  gh attestation verify --help >/dev/null 2>&1 || fail 'gh attestation verify is unavailable. Ask the user for consent to upgrade GitHub CLI from a trusted source.'
else
  # Legacy bundled hash metadata is retained for local regression fixtures.
  entry=$(awk -v platform="$os-$arch" 'NR > 1 && $1 == platform { print $2 " " $3; n++ } END { if (n != 1) exit 1 }' "$root/release.tsv") || fail 'platform has no unique pinned artifact'
  url=${entry% *}; digest=${entry##* }
  [ "${#digest}" -eq 64 ] || fail 'invalid pinned SHA256'
  case "$digest" in *[!0-9a-f]*) fail 'invalid pinned SHA256' ;; esac
  case "$url" in https://*) ;; http://127.0.0.1:*)
    [ "${CHRONICLE_TEST_ALLOW_HTTP:-}" = 1 ] || fail 'artifact requires HTTPS' ;;
    *) fail 'artifact requires HTTPS' ;;
  esac
fi
data=${CLAUDE_PLUGIN_DATA:-}
case "$data" in /*) ;; *) fail 'CLAUDE_PLUGIN_DATA must be an absolute private directory' ;; esac
# Reject traversal and symlink components, including existing ancestors.
case "$data/" in *'/../'*|*'/./'*|*'//'*) fail 'unsafe plugin data path' ;; esac
check=$data
while [ "$check" != / ]; do
  [ ! -L "$check" ] || fail 'plugin data path must not contain symlinks'
  check=$(dirname -- "$check")
done
[ "$data" != / ] || fail 'plugin data must not be the filesystem root'
[ "$action" = start ] || [ -d "$data/bin/$version" ] || fail 'pinned binary is not installed; run setup first'
mkdir -p -- "$data"
[ "$(ls -nd "$data" | awk '{print $3}')" = "$(id -u)" ] || fail 'plugin data must be owned by the current user'
# Do not use shared/writable-by-others directories as executable storage.
[ "$action" != start ] || chmod 700 "$data"
for dir in "$data/bin" "$data/bin/$version"; do
  [ ! -L "$dir" ] || fail 'binary directory must not be a symlink'
  mkdir -p -- "$dir"
done
bin=$data/bin/$version/chronicle-mcp
[ ! -L "$bin" ] || fail 'binary must not be a symlink'
hash() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
  else fail 'sha256sum or shasum is required'; fi
}
verify() {
  if [ "$state" = attested ]; then
    # Keep verifier output off stdout: headers is a private JSON protocol.
    gh attestation verify "$1" --repo Emyrk/chronicle-mcp \
      --signer-workflow Emyrk/chronicle-mcp/.github/workflows/release.yml \
      --source-ref "refs/tags/$version" --deny-self-hosted-runners >&2 ||
      fail 'artifact attestation verification failed; refusing execution. Check network/authentication and gh support for the required flags; ask user consent before upgrading gh. No unverified fallback is allowed.'
  else
    [ "$(hash "$1")" = "$digest" ] || fail "$2 SHA256 mismatch; refusing execution (remove it explicitly to reinstall)"
  fi
}
if [ ! -f "$bin" ]; then
  [ "$action" = start ] || fail 'pinned binary is not installed; run setup first'
  lock=$data/.install-lock
  mkdir "$lock" 2>/dev/null || fail 'installation already in progress (inspect a stale .install-lock manually)'
  tmp=
  trap 'test -z "$tmp" || rm -f -- "$tmp"; rmdir -- "$lock"' 0
  trap 'exit 1' HUP INT TERM
  tmp=$(mktemp "$data/bin/$version/.download.XXXXXXXX")
  command -v curl >/dev/null 2>&1 || fail 'curl is required'
  # HTTPS redirects only; loopback HTTP is an explicit test-only escape hatch.
  protocols='=https'
  [ "$state" != released ] || [ "${CHRONICLE_TEST_ALLOW_HTTP:-}" != 1 ] || protocols='=http,https'
  curl --fail --silent --show-error --location --proto "$protocols" --proto-redir "$protocols" --connect-timeout 15 --max-time 120 --output "$tmp" "$url" || fail "could not download pinned release $version; it may not be published yet. No fallback is allowed."
  verify "$tmp" artifact
  chmod 700 "$tmp"
  mv -- "$tmp" "$bin"
  tmp=
  rmdir -- "$lock"
  trap - 0 HUP INT TERM
fi
verify "$bin" 'installed binary'
exec "$bin" "$action" "$@"
