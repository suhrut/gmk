#!/usr/bin/env bash
# scripts/read-key.sh
#
# Helper for reading secrets out of ~/.gmk/<project>/keys (preferred)
# or ~/.gmk/keys (global fallback). Source this into a target body
# and call read_key.
#
# Schema (JSON):
#
#   {
#     "<profile>": {
#       "<name>": "raw-string",                              # bare form
#       "<name>": { "password": "...", "algorithm": "..." }  # object form
#     }
#   }
#
# Both string and object values are accepted under the same key name;
# the reader extracts .password from objects. The "algorithm" field
# (and any other future metadata) is documented in the schema but
# not currently consumed.
#
# Usage:
#
#   source scripts/read-key.sh
#   pwfile=$(read_key ca "$key_profile")
#   trap 'rm -f "$pwfile"' EXIT
#   openssl ... -passin "file:$pwfile"
#
# The function:
#   - Prints the temp file path to stdout on success (so caller does
#     pwfile=$(read_key ...))
#   - Writes all errors and help text to stderr
#   - Returns 1 on any failure (missing file, missing profile,
#     missing key, invalid JSON, jq not installed)
#
# Env vars consulted:
#   GMK_KEY_PROJECT  — if set and ~/.gmk/$GMK_KEY_PROJECT/keys exists,
#                      that file is used; otherwise falls through to
#                      the global ~/.gmk/keys.
#
# Requires: jq.

read_key() {
  local name="${1:-}"
  local profile="${2:-default}"

  if [ -z "$name" ]; then
    echo "read_key: missing key name (usage: read_key <name> [<profile>])" >&2
    return 1
  fi

  if ! command -v jq >/dev/null 2>&1; then
    echo "read_key: jq not found in PATH" >&2
    echo "    install: apt-get install jq  /  brew install jq" >&2
    return 1
  fi

  # 1. Locate the keys file. Project-specific takes precedence.
  local keys_file=""
  if [ -n "${GMK_KEY_PROJECT:-}" ] && [ -f "$HOME/.gmk/$GMK_KEY_PROJECT/keys" ]; then
    keys_file="$HOME/.gmk/$GMK_KEY_PROJECT/keys"
  elif [ -f "$HOME/.gmk/keys" ]; then
    keys_file="$HOME/.gmk/keys"
  else
    _read_key_print_setup_help "$name" "$profile" >&2
    return 1
  fi

  # 2. Permission sanity check. Warn only.
  local perms
  perms=$(stat -c '%a' "$keys_file" 2>/dev/null || stat -f '%Lp' "$keys_file" 2>/dev/null)
  case "$perms" in
    600|0600) ;;
    *) echo "read_key: WARNING: $keys_file permissions are $perms; recommend 600" >&2 ;;
  esac

  # 3. JSON validity.
  if ! jq empty "$keys_file" 2>/dev/null; then
    echo "read_key: $keys_file is not valid JSON" >&2
    return 1
  fi

  # 4. Profile presence.
  if [ "$(jq -r --arg p "$profile" 'has($p)' "$keys_file")" != "true" ]; then
    echo "read_key: profile \"$profile\" not found in $keys_file" >&2
    local available
    available=$(jq -r 'keys | join(", ")' "$keys_file")
    echo "    available profiles: $available" >&2
    return 1
  fi

  # 5. Extract the value. Accepts both bare-string and {password:...}
  #    object forms under the same key name.
  local value
  value=$(jq -r --arg p "$profile" --arg n "$name" '
    .[$p][$n] // null
    | if type == "object" then (.password // null) else . end
  ' "$keys_file")

  if [ -z "$value" ] || [ "$value" = "null" ]; then
    echo "read_key: key \"$name\" not found under profile \"$profile\" in $keys_file" >&2
    local avail_keys
    avail_keys=$(jq -r --arg p "$profile" '.[$p] | keys | join(", ")' "$keys_file" 2>/dev/null)
    [ -n "$avail_keys" ] && echo "    available keys in that profile: $avail_keys" >&2
    return 1
  fi

  # 6. Write to a 0600 temp file. Caller is responsible for cleanup
  #    via trap (see header).
  local tmpfile
  tmpfile=$(mktemp) || return 1
  chmod 600 "$tmpfile"
  printf '%s' "$value" > "$tmpfile"
  printf '%s' "$tmpfile"
}

# Internal: helpful "no keys file found" message printed to stderr.
_read_key_print_setup_help() {
  local name="$1" profile="$2"
  local proj="${GMK_KEY_PROJECT:-}"
  echo "read_key: no keys file found." >&2
  echo "" >&2
  echo "Create one in JSON form. Example schema:" >&2
  echo "" >&2
  echo '  {' >&2
  echo '    "default": {' >&2
  echo '      "ca":     "a-passphrase",' >&2
  echo '      "sqlite": "another-passphrase"' >&2
  echo '    },' >&2
  echo '    "prod": {' >&2
  echo '      "ca":     { "password": "prod-passphrase", "algorithm": "aes256" },' >&2
  echo '      "sqlite": { "password": "prod-sqlite-passphrase" }' >&2
  echo '    }' >&2
  echo '  }' >&2
  echo "" >&2
  if [ -n "$proj" ]; then
    echo "Project-specific location (preferred for this project):" >&2
    echo "  mkdir -p ~/.gmk/$proj && chmod 700 ~/.gmk/$proj" >&2
    echo "  \$EDITOR ~/.gmk/$proj/keys && chmod 600 ~/.gmk/$proj/keys" >&2
    echo "" >&2
    echo "Or the global fallback (shared across projects):" >&2
  else
    echo "Global location:" >&2
  fi
  echo "  mkdir -p ~/.gmk && chmod 700 ~/.gmk" >&2
  echo "  \$EDITOR ~/.gmk/keys && chmod 600 ~/.gmk/keys" >&2
  echo "" >&2
  echo "Then re-run. Looking for key \"$name\" in profile \"$profile\"." >&2
}
