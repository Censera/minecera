#!/usr/bin/env bash
set -euo pipefail

PLUGINS_DIR="plugins"
DATAPACKS_DIR="world/datapacks"

mkdir -p "$PLUGINS_DIR" "$DATAPACKS_DIR"

download_list() {
    local list="$1"
    local destination="$2"

    while IFS= read -r line || [ -n "$line" ]; do
        [ -z "$line" ] && continue

        name="${line%% (*}"
        url="${line#*(}"
        url="${url%)}"

        if [ "$name" = "$line" ] || [ "$url" = "$line" ] || [ -z "$name" ] || [ -z "$url" ]; then
            echo "invalid entry: $line" >&2
            return 1
        fi

        echo "  $name"
        curl -fsSL --retry 3 -- "$url" -o "$destination/$name"
    done < "$list"
}

echo "Downloading plugins..."
download_list "$PLUGINS_DIR/plugin.list" "$PLUGINS_DIR"

echo "Downloading datapacks..."
download_list "$DATAPACKS_DIR/datapacks.list" "$DATAPACKS_DIR"

echo "Done."
