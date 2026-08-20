#!/usr/bin/env bash
set -e

ROOT="${MINECERA_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}"
JAR="${MINECERA_JAR:-$ROOT/paper-26.2.jar}"
XMS="${MINECERA_XMS:-6G}"
XMX="${MINECERA_XMX:-6G}"

cd "$ROOT"

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
