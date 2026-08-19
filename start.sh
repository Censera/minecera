#!/usr/bin/env bash
set -e

ROOT="${MINECERA_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}"
JAR="${MINECERA_JAR:-$ROOT/paper-26.2.jar}"

cd "$ROOT"

exec java \
    -Xms6G \
    -Xmx6G \
    -XX:+UseG1GC \
    -XX:+ParallelRefProcEnabled \
    -XX:MaxGCPauseMillis=200 \
    -XX:+DisableExplicitGC \
    -XX:+AlwaysPreTouch \
    -XX:+PerfDisableSharedMem \
    -jar "$JAR" \
    --nogui
