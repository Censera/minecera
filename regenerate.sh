#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONTROL="$ROOT/run/control"

if [ -p "$CONTROL" ]; then
    printf '%s\n' stop > "$CONTROL"
    for _ in {1..60}; do
        if ! pgrep -f '(^|/)java .*paper-26\.2\.jar' >/dev/null 2>&1; then
            break
        fi
        sleep 1
done
fi

if pgrep -f '(^|/)java .*paper-26\.2\.jar' >/dev/null 2>&1; then
    echo "Minecraft did not stop" >&2
    exit 1
fi

TMP=$(mktemp -d)
trap 'rm -rf -- "$TMP"' EXIT

cp -a world/datapacks "$TMP/datapacks"
rm -rf -- world
mkdir -p world
cp -a "$TMP/datapacks" world/

exec ./start.sh
