#!/usr/bin/env bash
set -e

PLUGINS_DIR="plugins"
DATAPACKS_DIR="world/datapacks"

mkdir -p "$PLUGINS_DIR" "$DATAPACKS_DIR"

echo "Downloading plugins..."
while IFS= read -r line; do
    name=$(echo "$line" | grep -oP '^[^ (]+')
    url=$(echo "$line" | grep -oP 'https://[^)]+')
    echo "  $name"
    wget -q "$url" -O "$PLUGINS_DIR/$name"
done < "$PLUGINS_DIR/plugin.list"

echo "Downloading datapacks..."
while IFS= read -r line; do
    name=$(echo "$line" | grep -oP '^[^(]+' | xargs)
    url=$(echo "$line" | grep -oP 'https://[^)]+')
    echo "  $name"
    wget -q "$url" -O "$DATAPACKS_DIR/$name"
done < "$DATAPACKS_DIR/datapacks.list"

echo "Done."
