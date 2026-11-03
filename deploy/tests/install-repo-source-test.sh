#!/bin/bash
# Checks how deploy/install.sh resolves its release source: the built-in
# upstream default, the GITHUB_REPO environment, and --repo (via
# set_github_repo), and that the chosen source actually reaches the GitHub URLs.

set -euo pipefail

# install.sh itself requires Bash 4+ (associative-array message table), and it
# exits early when sourced by an older shell, so skip instead of failing.
if [ -z "${BASH_VERSION:-}" ] || [ "${BASH_VERSION%%.*}" -lt 4 ]; then
    echo "SKIP: install-repo-source-test.sh needs Bash 4+ (found ${BASH_VERSION:-non-bash})"
    exit 0
fi

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TEMP_DIR=$(mktemp -d)
trap 'rm -rf "$TEMP_DIR"' EXIT

INSTALL_SH="$ROOT_DIR/deploy/install.sh"
UPSTREAM="Wei-Shaw/sub2api"
FORK="fork-owner/sub2api"

mkdir -p "$TEMP_DIR/home"
: > "$TEMP_DIR/home/.curlrc"

# Fake curl: record argv, return no body so version_exists stops after the URL
# has been logged.
cat > "$TEMP_DIR/curl" <<'EOF'
#!/bin/bash
printf '%s\n' "$@" > "$CURL_ARGS_LOG"
EOF
chmod +x "$TEMP_DIR/curl"

# Load install.sh helpers without running main "$@" (the last line).
# `sed '$d'` instead of `head -n -1`: BSD head rejects negative line counts.
LOAD='source <(sed '\''$d'\'' "$1")'

resolve_repo() { # $1 = GITHUB_REPO value to export ("" leaves it unset)
    local value="$1"
    if [ -n "$value" ]; then
        GITHUB_REPO="$value" bash -c "$LOAD; printf '%s\n' \"\$GITHUB_REPO\"" bash "$INSTALL_SH"
    else
        bash -c "$LOAD; printf '%s\n' \"\$GITHUB_REPO\"" bash "$INSTALL_SH"
    fi
}

# Default stays upstream; GITHUB_REPO overrides it.
test "$(resolve_repo '')" = "$UPSTREAM"
test "$(resolve_repo "$FORK")" = "$FORK"

# A malformed GITHUB_REPO must fail fast rather than build odd URLs.
if GITHUB_REPO="not a repo" bash -c "$LOAD" bash "$INSTALL_SH" >/dev/null 2>&1; then
    echo "installer accepted a malformed GITHUB_REPO" >&2
    exit 1
fi

# set_github_repo (--repo) validates and applies.
bash -c "$LOAD
set_github_repo '$FORK'
[ \"\$GITHUB_REPO\" = '$FORK' ] || { echo '--repo value was not applied' >&2; exit 1; }
set_github_repo 'bad value' >/dev/null 2>&1 && { echo '--repo accepted a malformed value' >&2; exit 1; }
[ \"\$GITHUB_REPO\" = '$FORK' ] || { echo 'rejected --repo value mutated GITHUB_REPO' >&2; exit 1; }
" bash "$INSTALL_SH"

# Unknown long options must fail before language selection or any install work.
# This catches typos and prevents silently ignoring unsupported source options.
if bash -c "$LOAD
select_language() { echo 'argument parsing reached language selection' >&2; exit 97; }
main rollback v0.1.176 --unknown-repo '$FORK'
" bash "$INSTALL_SH" >"$TEMP_DIR/unknown-option" 2>&1; then
    echo 'installer accepted an unknown long option' >&2
    exit 1
else
    status=$?
    test "$status" -eq 1
    grep -Fq -- 'Error: unknown option: --unknown-repo' "$TEMP_DIR/unknown-option"
fi

# The source must reach the release API URL, not stay pinned to upstream.
check_api_url() { # $1 = expected repo, $2 = extra setup
    local log="$TEMP_DIR/curlargs.${1//\//_}"
    CURL_ARGS_LOG="$log" HOME="$TEMP_DIR/home" PATH="$TEMP_DIR:$PATH" \
        bash -c "$LOAD
$2
version_exists v0.1.177 >/dev/null 2>&1 || true
" bash "$INSTALL_SH"
    grep -Fxq -- "https://api.github.com/repos/$1/releases/tags/v0.1.177" "$log" || {
        echo "release lookup did not use $1" >&2
        cat "$log" >&2
        exit 1
    }
}

check_api_url "$UPSTREAM" ""
check_api_url "$FORK" "set_github_repo '$FORK'"

echo "install.sh release source checks passed"
