#!/usr/bin/env bash
set -e

exec java \
    -Xms6G \
    -Xmx6G \
    -XX:+UseG1GC \
    -XX:+ParallelRefProcEnabled \
    -XX:MaxGCPauseMillis=200 \
    -XX:+DisableExplicitGC \
    -XX:+AlwaysPreTouch \
    -XX:+PerfDisableSharedMem \
    -jar paper-26.2.jar \
    --nogui
