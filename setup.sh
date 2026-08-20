#!/usr/bin/env bash
set -euo pipefail

PLUGINS_DIR="plugins"
DATAPACKS_DIR="world/datapacks"

mkdir -p "$PLUGINS_DIR" "$DATAPACKS_DIR"

download_list() {
    local list="$1"
    local destination="$2"
    local line name url tmp

    while IFS= read -r line || [ -n "$line" ]; do
        [[ -z "$line" || "$line" =~ ^[[:space:]]*# ]] && continue

        if [[ ! "$line" =~ ^(.+)[[:space:]]\((https?://[^)]*)\)$ ]]; then
            echo "invalid entry: $line" >&2
            return 1
        fi

        name="${BASH_REMATCH[1]}"
        url="${BASH_REMATCH[2]}"
        tmp="$destination/.${name}.part"

        printf '  %-32s' "$name"
        rm -f -- "$tmp"

        if curl --fail --silent --show-error --location \
            --retry 5 --retry-delay 1 --retry-all-errors \
            --connect-timeout 15 --max-time 300 \
            --output "$tmp" -- "$url"; then
            if [[ ! -s "$tmp" ]]; then
                rm -f -- "$tmp"
                echo "FAILED (empty response)" >&2
                return 1
            fi
            mv -f -- "$tmp" "$destination/$name"
            echo "ok"
        else
            rm -f -- "$tmp"
            echo "FAILED" >&2
            echo "    $url" >&2
            return 1
        fi
    done < "$list"
}

echo "Downloading plugins..."
download_list "$PLUGINS_DIR/plugin.list" "$PLUGINS_DIR"

echo "Downloading datapacks..."
download_list "$DATAPACKS_DIR/datapacks.list" "$DATAPACKS_DIR"
echo "Done."
