// lib.sh generator. Each materialized bash/sh body sources lib.sh,
// which provides the standard helpers that read prelude.json and
// args.json:
//
//   p_get NAME [DEFAULT]   — prelude string value (dot path supported)
//   a_get NAME [DEFAULT]   — args string value (dot path supported)
//   p_get_json NAME        — prelude raw JSON (for structured values)
//   a_get_json NAME        — args raw JSON
//   p_keys                 — space-separated keys of the prelude map
//   a_keys                 — space-separated keys of the args map
//   p_has NAME             — exit 0 if prelude has key, 1 otherwise
//   a_has NAME             — same for args
//   r_set VALUE            — set result.json to a JSON-encoded string
//   r_set_json JSON        — set result.json to raw JSON
//   r_set_path KEY VALUE   — patch one field of result (init to {} if needed)
//   log_info MSG [k=v ...] — emit a structured info log line to GMK_LOG_FILE
//   log_debug, log_warn, log_error — same at other levels
//
// All helpers expect $GMK_ARGS, $GMK_PRELUDE, $GMK_RESULT to be set
// (which the runner does). They use jq when present and a fallback
// best-effort parser when not, so simple flat-map cases still work
// without jq.

package materialize

const libShContents = `# gmk bash helper library. Sourced automatically by every materialized
# bash/sh body. Provides helpers for reading $GMK_ARGS / $GMK_PRELUDE
# and writing $GMK_RESULT.
#
# Generated; do not edit.

set +e  # Helpers handle their own errors; don't blow up the calling script.

# _gmk_have_jq returns 0 if jq is on PATH, 1 otherwise.
_gmk_have_jq() {
  command -v jq >/dev/null 2>&1
}

# _gmk_read_json_field reads .NAME from FILE, with optional default.
# Args: $1=file, $2=name (dot path), $3=default (optional)
_gmk_read_json_field() {
  local file="$1" name="$2" default="${3:-}"
  if [ ! -f "$file" ]; then
    printf '%s' "$default"
    return 0
  fi
  if _gmk_have_jq; then
    local val
    val=$(jq -r --arg d "$default" ".${name} // \$d" "$file" 2>/dev/null)
    printf '%s' "$val"
  else
    # Fallback: only flat top-level fields. Anything nested needs jq.
    local val
    val=$(grep -o "\"${name}\"[ ]*:[ ]*\"[^\"]*\"" "$file" 2>/dev/null | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
    if [ -z "$val" ]; then
      # Try unquoted (numbers, bools, null).
      val=$(grep -o "\"${name}\"[ ]*:[ ]*[^,}\"]*" "$file" 2>/dev/null | head -1 | sed "s/.*:[ ]*//" | tr -d '[:space:]')
    fi
    if [ -z "$val" ] || [ "$val" = "null" ]; then
      printf '%s' "$default"
    else
      printf '%s' "$val"
    fi
  fi
}

# _gmk_read_json_field_raw reads .NAME from FILE as raw JSON (no -r).
_gmk_read_json_field_raw() {
  local file="$1" name="$2"
  if _gmk_have_jq; then
    jq -c ".${name}" "$file" 2>/dev/null
  else
    printf 'null'  # without jq we can't safely return structured JSON
  fi
}

# p_get NAME [DEFAULT] — read .NAME from $GMK_PRELUDE as a string.
p_get() { _gmk_read_json_field "$GMK_PRELUDE" "$1" "${2:-}"; }

# a_get NAME [DEFAULT] — read .NAME from $GMK_ARGS as a string.
a_get() { _gmk_read_json_field "$GMK_ARGS" "$1" "${2:-}"; }

# p_get_json NAME — read .NAME from $GMK_PRELUDE as raw JSON.
p_get_json() { _gmk_read_json_field_raw "$GMK_PRELUDE" "$1"; }

# a_get_json NAME — read .NAME from $GMK_ARGS as raw JSON.
a_get_json() { _gmk_read_json_field_raw "$GMK_ARGS" "$1"; }

# p_keys — print space-separated top-level keys of the prelude map.
p_keys() {
  if _gmk_have_jq; then
    jq -r 'keys[]' "$GMK_PRELUDE" 2>/dev/null | tr '\n' ' '
  else
    grep -o '"[^"]*"[ ]*:' "$GMK_PRELUDE" 2>/dev/null | sed 's/"\([^"]*\)".*/\1/' | tr '\n' ' '
  fi
}

# a_keys — print space-separated top-level keys of the args map.
a_keys() {
  if _gmk_have_jq; then
    jq -r 'keys[]' "$GMK_ARGS" 2>/dev/null | tr '\n' ' '
  else
    grep -o '"[^"]*"[ ]*:' "$GMK_ARGS" 2>/dev/null | sed 's/"\([^"]*\)".*/\1/' | tr '\n' ' '
  fi
}

# p_has NAME — exit 0 if prelude has the named key, 1 otherwise.
p_has() {
  if _gmk_have_jq; then
    jq -e --arg n "$1" 'has($n)' "$GMK_PRELUDE" >/dev/null 2>&1
  else
    grep -q "\"$1\"[ ]*:" "$GMK_PRELUDE" 2>/dev/null
  fi
}

# a_has NAME — same, for args.
a_has() {
  if _gmk_have_jq; then
    jq -e --arg n "$1" 'has($n)' "$GMK_ARGS" >/dev/null 2>&1
  else
    grep -q "\"$1\"[ ]*:" "$GMK_ARGS" 2>/dev/null
  fi
}

# r_set VALUE — set $GMK_RESULT to the JSON-encoded version of VALUE.
# Treats VALUE as a string.
r_set() {
  if _gmk_have_jq; then
    jq -n --arg v "$1" '$v' > "$GMK_RESULT"
  else
    # Naive escaping: backslash, quote, newline.
    local v="$1"
    v="${v//\\/\\\\}"
    v="${v//\"/\\\"}"
    v="${v//$'\n'/\\n}"
    printf '"%s"\n' "$v" > "$GMK_RESULT"
  fi
}

# r_set_json JSON — set $GMK_RESULT to the raw JSON (validated if jq is present).
r_set_json() {
  if _gmk_have_jq; then
    printf '%s\n' "$1" | jq '.' > "$GMK_RESULT"
  else
    printf '%s\n' "$1" > "$GMK_RESULT"
  fi
}

# r_set_path KEY VALUE — set a single field in the result object, treating
# VALUE as a JSON-encoded string. If $GMK_RESULT is currently null or
# missing, initializes to {}.
r_set_path() {
  local key="$1" val="$2"
  if _gmk_have_jq; then
    local current
    current=$(cat "$GMK_RESULT" 2>/dev/null || echo "null")
    if [ "$(echo "$current" | jq 'type')" = '"null"' ]; then
      current="{}"
    fi
    echo "$current" | jq --arg k "$key" --arg v "$val" '.[$k] = $v' > "$GMK_RESULT"
  else
    # Best effort without jq: just append the key. Not idempotent — caller
    # should prefer setting the whole result via r_set_json.
    printf '{"%s":"%s"}\n' "$key" "$val" > "$GMK_RESULT"
  fi
}

# _gmk_log_to_stderr LEVEL MSG ARGS — log to stderr only when no GMK_LOG_FILE.
_gmk_log_emit() {
  local level="$1" msg="$2"
  shift 2
  # Build a tiny JSON line: {"level":"info","name":"gmk.target.X","msg":"..."}.
  local line
  if _gmk_have_jq; then
    line=$(jq -nc --arg L "$level" --arg N "${GMK_KIND:-?}:${GMK_NAME:-?}" --arg M "$msg" \
      '{level: $L, name: ("gmk." + $N | gsub(":"; ".")), msg: $M}')
  else
    line="{\"level\":\"$level\",\"name\":\"gmk.${GMK_KIND:-?}.${GMK_NAME:-?}\",\"msg\":\"$msg\"}"
  fi
  if [ -n "${GMK_LOG_FILE:-}" ]; then
    printf '%s\n' "$line" >> "$GMK_LOG_FILE"
  else
    printf '%s\n' "$line" >&2
  fi
}

log_trace() { _gmk_log_emit trace "$@"; }
log_debug() { _gmk_log_emit debug "$@"; }
log_info()  { _gmk_log_emit info  "$@"; }
log_warn()  { _gmk_log_emit warn  "$@"; }
log_error() { _gmk_log_emit error "$@"; }

# Re-enable strict mode if the calling script wants it. Bodies that opt
# out of strict mode can shopt -uo errexit etc. above this source line.
set -e 2>/dev/null || true
`

// LibShContents returns the full lib.sh source. Exposed for tests and
// for the `gmk doc --shell-helpers` command.
func LibShContents() string { return libShContents }
