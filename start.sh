#!/usr/bin/env bash
set -e

ROOT="${MINECERA_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}"
JAR="${MINECERA_JAR:-$ROOT/paper-26.2.jar}"
XMS="${MINECERA_XMS:-6G}"
XMX="${MINECERA_XMX:-6G}"
MOTD_LIST="$ROOT/motd/list.motd"

cd "$ROOT"

if [ -f "$MOTD_LIST" ]; then
    command -v chmotd >/dev/null 2>&1 || {
        printf 'minecera: motd/list.motd exists but chmotd is not installed\n' >&2
        exit 1
    }

    printf '[%s] applying MOTD configuration\n' "$(date '+%F %T')"
    chmotd "$MOTD_LIST" "$ROOT"
fi

exec java \
    -Xms"$XMS" \
    -Xmx"$XMX" \
    -XX:+UseG1GC \
    -XX:+ParallelRefProcEnabled \
    -XX:MaxGCPauseMillis=200 \
    -XX:+DisableExplicitGC \
    -XX:+AlwaysPreTouch \
    -XX:+PerfDisableSharedMem \
    -jar "$JAR" \
    --nogui
