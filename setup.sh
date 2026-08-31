#!/usr/bin/env bash
set -euo pipefail
shopt -s extglob

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
USER_AGENT="Minecera/1.0 (https://github.com/Censera/minecera)"

RESET='\033[0m'
BOLD='\033[1m'
GREEN='\033[32m'
RED='\033[31m'
YELLOW='\033[33m'
CYAN='\033[36m'

LIST_DESTINATION=""
INCLUDE_NAMES=()
INCLUDE_URLS=()
EXCLUDE_NAMES=()
EXTENSIONS=()

report() {
    printf '%b\n' "$*"
}

status() {
    local label="$1"
    local color="$2"
    local result="$3"
    printf '    %-36s [%b%s%b]\n' "$label" "$color" "$result" "$RESET"
}

discover_lists() {
    mapfile -d '' LISTS < <(
        find "$ROOT/lists" -maxdepth 1 -type f -name '*.list' -print0 | sort -z
    )

    if [ "${#LISTS[@]}" -eq 0 ]; then
        echo "no list manifests found in $ROOT/lists" >&2
        return 1
    fi
}

parse_list() {
    local list="$1"
    local line line_number=0
    local destination_set=0
    local destination_re='^destination[[:space:]]+"([^"]+)"$'
    local entry_re='^([+-])[[:space:]]+([^()]+\.[^()[:space:]]+)[[:space:]]+\((https?://[^)]+)\)$'
    local action name url extension
    declare -A seen_names=()
    declare -A seen_extensions=()

    LIST_DESTINATION=""
    INCLUDE_NAMES=()
    INCLUDE_URLS=()
    EXCLUDE_NAMES=()
    EXTENSIONS=()

    while IFS= read -r line || [ -n "$line" ]; do
        line_number=$((line_number + 1))
        line="${line%$'\r'}"
        line="${line%%#*}"
        line="${line##+([[:space:]])}"
        line="${line%%+([[:space:]])}"

        [[ -n "$line" ]] || continue

        if [ "$destination_set" -eq 0 ]; then
            if [[ $line =~ $destination_re ]]; then
                LIST_DESTINATION="${BASH_REMATCH[1]}"
                destination_set=1
            else
                echo "$list:$line_number: expected: destination \"to/path\"" >&2
                return 1
            fi
            continue
        fi

        if [[ ! $line =~ $entry_re ]]; then
            echo "$list:$line_number: expected: + file.extension (direct-download-link) or - file.extension (direct-download-link)" >&2
            return 1
        fi

        action="${BASH_REMATCH[1]}"
        name="${BASH_REMATCH[2]}"
        url="${BASH_REMATCH[3]}"
        extension="${name##*.}"

        if [[ -n "${seen_names[$name]+x}" ]]; then
            echo "$list:$line_number: duplicate entry: $name" >&2
            return 1
        fi
        seen_names["$name"]=1

        if [[ -z "${seen_extensions[$extension]+x}" ]]; then
            EXTENSIONS+=("$extension")
            seen_extensions["$extension"]=1
        fi

        if [ "$action" = "+" ]; then
            INCLUDE_NAMES+=("$name")
            INCLUDE_URLS+=("$url")
        else
            EXCLUDE_NAMES+=("$name")
        fi
    done < "$list"

    [ "$destination_set" -eq 1 ] || {
        echo "$list: missing destination declaration" >&2
        return 1
    }

    case "$LIST_DESTINATION" in
        /*|..|../*|*/../*|*/..)
            echo "$list: destination must stay inside Minecera" >&2
            return 1
            ;;
    esac

    mkdir -p "$ROOT/$LIST_DESTINATION"
}

clean_list() {
    local destination="$ROOT/$LIST_DESTINATION"
    local extension file name
    declare -A wanted=()

    for name in "${INCLUDE_NAMES[@]}"; do
        wanted["$name"]=1
    done

    for name in "${EXCLUDE_NAMES[@]}"; do
        file="$destination/$name"
        if [ -f "$file" ]; then
            rm -f -- "$file"
        fi
    done

    for extension in "${EXTENSIONS[@]}"; do
        while IFS= read -r -d '' file; do
            name="$(basename -- "$file")"
            if [[ -z "${wanted[$name]+x}" ]]; then
                rm -f -- "$file"
            fi
        done < <(find "$destination" -maxdepth 1 -type f -name "*.$extension" -print0)
    done
}

remote_download() {
    local url="$1"
    local output="$2"

    if [[ "$url" == *drive.google.com/* || "$url" == *drive.usercontent.google.com/* ]]; then
        if command -v gdown >/dev/null 2>&1; then
            gdown "$url" -O "$output"
            return $?
        fi
        if python3 -c 'import gdown' >/dev/null 2>&1; then
            python3 -m gdown "$url" -O "$output"
            return $?
        fi
        echo "Google Drive download requires gdown: $url" >&2
        return 1
    fi

    curl --fail --silent --show-error --location \
        --retry 5 --retry-delay 1 --retry-all-errors \
        --connect-timeout 15 --max-time 300 \
        --user-agent "$USER_AGENT" \
        --output "$output" -- "$url"
}

valid_download() {
    local file="$1"
    [ -s "$file" ] || return 1
    case "$(head -c 2 -- "$file")" in
        PK) return 0 ;;
        *)  return 1 ;;
    esac
}

download_file() {
    local name="$1"
    local url="$2"
    local destination="$ROOT/$LIST_DESTINATION"
    local target="$destination/$name"
    local temp="$destination/.${name}.part"

    rm -f -- "$temp"

    if [ -f "$target" ] && valid_download "$target"; then
        status "$name" "$GREEN" "Done"
        return 0
    fi

    if remote_download "$url" "$temp" && valid_download "$temp"; then
        mv -f -- "$temp" "$target"
        status "$name" "$GREEN" "Done"
        return 0
    fi

    rm -f -- "$temp"
    status "$name" "$RED" "Fail"
    return 1
}

generate_forcepack_config() {
    local destination="$LIST_DESTINATION"
    local config="$ROOT/plugins/ForcePack/config.yml"
    local temp="$ROOT/plugins/ForcePack/.config.yml.part"
    local index name url clean_url hash

    [ "$destination" = "resourcepacks" ] || return 0
    [ -f "$ROOT/plugins/ForcePack.jar" ] || return 0

    mkdir -p "$ROOT/plugins/ForcePack"

    {
        cat <<'EOF'
# Generated by Minecera setup.sh.
# Source of truth: lists/resources.list
velocity-mode: false
prevent-movement: false
prevent-damage: false
enable-mc-164316-fix: true
load-last: false
await-items-adder-host: true
use-new-force-pack-screen: false
try-to-stop-fake-accept-hacks: false
send-loading-title: false
delay-pack-sending-by: 0
web-server:
  enabled: false
  protocol: "http://"
  server-ip: localhost
  port: 8080
  port-on-url: true
Server:
  packs:
    all:
      urls:
EOF

        for index in "${!INCLUDE_NAMES[@]}"; do
            name="${INCLUDE_NAMES[$index]}"
            url="${INCLUDE_URLS[$index]}"
            clean_url="${url%%\?*}"

            [ -f "$ROOT/$destination/$name" ] || {
                echo "missing downloaded resource pack: $ROOT/$destination/$name" >&2
                return 1
            }

            printf '        - "%s"\n' "$clean_url"
        done

        cat <<'EOF'
      generate-hash: false
      hashes:
EOF

        for index in "${!INCLUDE_NAMES[@]}"; do
            name="${INCLUDE_NAMES[$index]}"
            hash="$(sha1sum -- "$ROOT/$destination/$name" | cut -d ' ' -f1 | tr '[:lower:]' '[:upper:]')"
            printf '        - "%s"\n' "$hash"
        done

        cat <<'EOF'
  Actions:
    ACCEPTED:
      kick: false
      Commands: []
    DOWNLOADED:
      kick: false
      Commands: []
    SUCCESSFULLY_LOADED:
      kick: false
      Commands: []
    DECLINED:
      kick: false
      Commands: []
    FAILED_DOWNLOAD:
      kick: false
      Commands: []
    FAILED_RELOAD:
      kick: false
      Commands: []
    DISCARDED:
      kick: false
      Commands: []
  Update GUI Speed: 20
  Update GUI: true
  verify: true
  resend: true
  force-invalid-size: false
  geyser: false
  bypass-permission: false
  debug: false
EOF
    } > "$temp"

    mv -f -- "$temp" "$config"
    report "${BOLD}${CYAN}[ForcePack]${RESET} Config generated"
}

process_list() {
    local list="$1"
    local index
    local failed=0

    parse_list "$list"

    printf '\n%b[%s]%b\n' "$BOLD" "$(basename "$list")" "$RESET"

    clean_list

    for index in "${!INCLUDE_NAMES[@]}"; do
        if ! download_file "${INCLUDE_NAMES[$index]}" "${INCLUDE_URLS[$index]}"; then
            failed=1
        fi
    done

    for index in "${!EXCLUDE_NAMES[@]}"; do
        status "${EXCLUDE_NAMES[$index]}" "$YELLOW" "Skip"
    done

    generate_forcepack_config
    return "$failed"
}

discover_lists

names=()
for list in "${LISTS[@]}"; do
    names+=("$(basename "$list")")
done
printf '%bInstalling:%b %s\n' "$BOLD$CYAN" "$RESET" "$(IFS=', '; echo "${names[*]}")"

failed=0
for list in "${LISTS[@]}"; do
    if ! process_list "$list"; then
        failed=1
    fi
done

if [ "$failed" -eq 0 ]; then
    printf '\n%bSetup Complete%b\n' "$BOLD$GREEN" "$RESET"
else
    printf '\n%bSetup Complete with errors%b\n' "$BOLD$RED" "$RESET"
    exit 1
fi
