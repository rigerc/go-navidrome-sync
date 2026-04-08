#!/bin/bash
#
# Query Navidrome search3 for each local audio file and print raw song.Path hits.

set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
readonly DEFAULT_CONFIG_PATH="${REPO_ROOT}/config.yaml"
readonly DEFAULT_MUSIC_PATH="${REPO_ROOT}/testdata"
readonly API_VERSION="1.15.0"
readonly CLIENT_NAME="go-navidrome-ratings-sync"
readonly SONG_COUNT="5"

err() {
  echo "Error: $*" >&2
}

usage() {
  cat <<EOF
Usage: $(basename "$0") [--config PATH] [--music-path PATH] [--baseurl URL] [--user USER] [--password PASS]

Queries Navidrome search3 once per .mp3/.flac under the given music path and
prints the raw song.Path values returned for each local file.

Defaults:
  --config      ${DEFAULT_CONFIG_PATH}
  --music-path  ${DEFAULT_MUSIC_PATH}

Overrides may also be provided via environment variables:
  NAVIDROME_BASEURL
  NAVIDROME_USER
  NAVIDROME_PASSWORD
EOF
}

config_value() {
  local config_path="$1"
  local key="$2"

  awk -F': ' -v key="${key}" '
    $1 == key {
      value = substr($0, index($0, ":") + 1)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", value)
      gsub(/^"/, "", value)
      gsub(/"$/, "", value)
      print value
      exit
    }
  ' "${config_path}"
}

file_title() {
  local path="$1"
  local base
  base="$(basename "${path}")"
  printf '%s\n' "${base%.*}" | sed -E 's/^[0-9]+-?[[:space:]]*//'
}

auth_token() {
  local password="$1"
  local salt="$2"

  printf '%s%s' "${password}" "${salt}" | md5sum | awk '{print $1}'
}

query_file() {
  local file_path="$1"
  local rel_path="$2"
  local title="$3"
  local salt="$4"
  local token="$5"

  echo "FILE	${rel_path}"
  echo "QUERY	${title}"

  curl -sS "${BASEURL}/rest/search3" \
    --get \
    --data-urlencode "u=${USER_NAME}" \
    --data-urlencode "t=${token}" \
    --data-urlencode "s=${salt}" \
    --data-urlencode "v=${API_VERSION}" \
    --data-urlencode "c=${CLIENT_NAME}" \
    --data-urlencode "f=json" \
    --data-urlencode "query=${title}" \
    --data-urlencode "artistCount=0" \
    --data-urlencode "albumCount=0" \
    --data-urlencode "songCount=${SONG_COUNT}" \
  | jq -r '
      .["subsonic-response"].searchResult3.song // []
      | if length == 0 then
          "RESULT\t<none>"
        else
          .[]
          | "RESULT\t" + (.path // "") + "\tTITLE\t" + (.title // "")
        end
    '

  echo
}

main() {
  local config_path="${DEFAULT_CONFIG_PATH}"
  local music_path="${DEFAULT_MUSIC_PATH}"
  local arg

  while [[ $# -gt 0 ]]; do
    arg="$1"
    case "${arg}" in
      --config)
        config_path="$2"
        shift 2
        ;;
      --music-path)
        music_path="$2"
        shift 2
        ;;
      --baseurl)
        BASEURL="$2"
        shift 2
        ;;
      --user)
        USER_NAME="$2"
        shift 2
        ;;
      --password)
        PASSWORD="$2"
        shift 2
        ;;
      --help|-h)
        usage
        exit 0
        ;;
      *)
        err "unknown argument: ${arg}"
        usage
        exit 1
        ;;
    esac
  done

  if [[ ! -f "${config_path}" ]]; then
    err "config file not found: ${config_path}"
    exit 1
  fi
  if [[ ! -d "${music_path}" ]]; then
    err "music path not found: ${music_path}"
    exit 1
  fi

  BASEURL="${NAVIDROME_BASEURL:-${BASEURL:-$(config_value "${config_path}" "baseurl")}}"
  USER_NAME="${NAVIDROME_USER:-${USER_NAME:-$(config_value "${config_path}" "user")}}"
  PASSWORD="${NAVIDROME_PASSWORD:-${PASSWORD:-$(config_value "${config_path}" "password")}}"

  if [[ -z "${BASEURL}" || -z "${USER_NAME}" || -z "${PASSWORD}" ]]; then
    err "missing Navidrome credentials; use config, flags, or NAVIDROME_* env vars"
    exit 1
  fi

  if ! command -v curl >/dev/null 2>&1; then
    err "curl is required"
    exit 1
  fi
  if ! command -v jq >/dev/null 2>&1; then
    err "jq is required"
    exit 1
  fi
  if ! command -v md5sum >/dev/null 2>&1; then
    err "md5sum is required"
    exit 1
  fi
  if ! command -v openssl >/dev/null 2>&1; then
    err "openssl is required"
    exit 1
  fi

  local file_path
  local rel_path
  local title
  local salt
  local token

  while IFS= read -r -d '' file_path; do
    rel_path="${file_path#"${music_path}/"}"
    title="$(file_title "${file_path}")"
    salt="$(openssl rand -hex 8)"
    token="$(auth_token "${PASSWORD}" "${salt}")"
    query_file "${file_path}" "${rel_path}" "${title}" "${salt}" "${token}"
  done < <(find "${music_path}" -type f \( -iname '*.mp3' -o -iname '*.flac' \) -print0 | sort -z)
}

main "$@"
