#!/usr/bin/env bash
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JAR="$ROOT/paper-26.2.jar"
START="$ROOT/start.sh"
WORLD="$ROOT/world"
BACKUPS="$ROOT/backups"
RUN="$ROOT/run"
QUARANTINE="$RUN/quarantine"
LOG_DIR="$ROOT/logs"

START_TIMEOUT=180
COMMAND_TIMEOUT=15
STOP_TIMEOUT=120
MONITOR_INTERVAL=30
BACKUP_HOUR=04
KEEP_BACKUPS=3

SERVER_PID=""
SERVER_FD=""
SERVER_FIFO=""
SERVER_LOG=""
PROBE_PID=""
PROBE_FD=""
PROBE_FIFO=""
PROBE_LOG=""
SHUTTING_DOWN=0

report() {
    printf '[%s] %s\n' "$(date '+%F %T')" "$*" | tee -a "$LOG_DIR/supervisor.log"
    logger -t minecera -- "$*" 2>/dev/null || true
}

cleanup_probe() {
    if [ -n "$PROBE_PID" ] && kill -0 "$PROBE_PID" 2>/dev/null; then
        kill -INT "$PROBE_PID" 2>/dev/null || true
        for _ in $(seq 1 30); do
            kill -0 "$PROBE_PID" 2>/dev/null || break
            sleep 1
        done
        kill -KILL "$PROBE_PID" 2>/dev/null || true
    fi
    if [ -n "$PROBE_FD" ]; then
        eval "exec ${PROBE_FD}>&-" 2>/dev/null || true
        PROBE_FD=""
    fi
    if [ -n "$PROBE_FIFO" ]; then
        rm -f "$PROBE_FIFO"
        PROBE_FIFO=""
    fi
    PROBE_PID=""
}

cleanup_server() {
    if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
        kill -INT "$SERVER_PID" 2>/dev/null || true
        for _ in $(seq 1 "$STOP_TIMEOUT"); do
            kill -0 "$SERVER_PID" 2>/dev/null || break
            sleep 1
        done
        kill -KILL "$SERVER_PID" 2>/dev/null || true
    fi
    if [ -n "$SERVER_FD" ]; then
        eval "exec ${SERVER_FD}>&-" 2>/dev/null || true
        SERVER_FD=""
    fi
    if [ -n "$SERVER_FIFO" ]; then
        rm -f "$SERVER_FIFO"
        SERVER_FIFO=""
    fi
    SERVER_PID=""
}

shutdown() {
    [ "$SHUTTING_DOWN" -eq 1 ] && return
    SHUTTING_DOWN=1
    report "shutdown requested"
    cleanup_probe
    cleanup_server
    exit 0
}

trap shutdown INT TERM
trap cleanup_probe EXIT

ensure_dirs() {
    mkdir -p "$BACKUPS" "$QUARANTINE" "$LOG_DIR" "$RUN"
}

wait_for_log() {
    local log="$1"
    local pattern="$2"
    local timeout="$3"

    for _ in $(seq 1 "$timeout"); do
        grep -Eq "$pattern" "$log" 2>/dev/null && return 0
        [ -n "$SERVER_PID" ] && ! kill -0 "$SERVER_PID" 2>/dev/null && return 1
        [ -n "$PROBE_PID" ] && ! kill -0 "$PROBE_PID" 2>/dev/null && return 1
        sleep 1
    done
    return 1
}

command_ok() {
    local pid="$1"
    local fd="$2"
    local log="$3"
    local pattern="$4"
    local timeout="$5"
    local offset

    offset=$(wc -c < "$log")
    printf '%s\n' "list" >&"$fd" || return 1

    for _ in $(seq 1 "$timeout"); do
        tail -c +$((offset + 1)) "$log" 2>/dev/null | grep -Eq "$pattern" && return 0
        kill -0 "$pid" 2>/dev/null || return 1
        sleep 1
    done
    return 1
}

start_process() {
    local mode="$1"
    local cwd="$2"
    local log="$3"
    local fifo="$4"
    local fd_name="$5"

    rm -f "$fifo"
    mkfifo "$fifo"
    exec {fd}<->"$fifo"

    if [ "$mode" = "real" ]; then
        SERVER_PID=""
        bash "$START" <&"$fd" >"$log" 2>&1 &
        SERVER_PID=$!
        SERVER_FD="$fd"
        SERVER_FIFO="$fifo"
        SERVER_LOG="$log"
    else
        PROBE_PID=""
        (
            cd "$cwd" || exit 1
            exec java -Xms2G -Xmx2G -XX:+UseG1GC -XX:+ParallelRefProcEnabled -XX:MaxGCPauseMillis=200 \
                -XX:+DisableExplicitGC -XX:+PerfDisableSharedMem \
                -jar "$JAR" --nogui --nojline \
                -P "$cwd/plugins" \
                -C "$cwd/commands.yml" \
                -S "$cwd/spigot.yml" \
                -b "$cwd/bukkit.yml" \
                --paper-dir "$cwd/config" \
                -c "$cwd/server.properties" \
                -W "$cwd" \
                -h 127.0.0.1 -p 0
        ) <&"$fd" >"$log" 2>&1 &
        PROBE_PID=$!
        PROBE_FD="$fd"
        PROBE_FIFO="$fifo"
        PROBE_LOG="$log"
    fi
}

start_server() {
    local stamp
    stamp="$(date +%Y%m%d-%H%M%S)"
    SERVER_LOG="$LOG_DIR/server-$stamp.log"
    report "starting Minecraft"
    start_process real "$ROOT" "$SERVER_LOG" "$RUN/server.stdin" SERVER_FD

    if ! wait_for_log "$SERVER_LOG" 'Done \(' "$START_TIMEOUT"; then
        report "startup health check failed"
        cleanup_server
        return 1
    fi

    if ! command_ok "$SERVER_PID" "$SERVER_FD" "$SERVER_LOG" 'There are [0-9]+ of a max of [0-9]+ players online' "$COMMAND_TIMEOUT"; then
        report "startup command health check failed"
        cleanup_server
        return 1
    fi

    report "Minecraft healthy"
    return 0
}

stop_server() {
    [ -z "$SERVER_PID" ] && return 0
    kill -0 "$SERVER_PID" 2>/dev/null || {
        cleanup_server
        return 0
    }

    printf '%s\n' "stop" >&"$SERVER_FD" 2>/dev/null || true

    for _ in $(seq 1 "$STOP_TIMEOUT"); do
        kill -0 "$SERVER_PID" 2>/dev/null || {
            cleanup_server
            return 0
        }
        sleep 1
    done

    report "graceful stop timed out; forcing Minecraft down"
    kill -INT "$SERVER_PID" 2>/dev/null || true
    sleep 5
    kill -KILL "$SERVER_PID" 2>/dev/null || true
    cleanup_server
}

healthy_backup_dirs() {
    find "$BACKUPS" -mindepth 2 -maxdepth 2 -type f -name HEALTHY -printf '%h\n' 2>/dev/null | sort -r
}

prune_backups() {
    local count=0
    local dir
    while IFS= read -r dir; do
        [ -z "$dir" ] && continue
        count=$((count + 1))
        [ "$count" -le "$KEEP_BACKUPS" ] && continue
        report "removing old backup: $(basename "$dir")"
        rm -rf -- "$dir"
    done < <(healthy_backup_dirs)
}

prepare_probe() {
    local backup="$1"
    local probe="$RUN/probe"

    cleanup_probe
    rm -rf -- "$probe"
    mkdir -p "$probe/plugins"
    cp -a "$backup/world" "$probe/world"
    cp -a "$ROOT/server.properties" "$probe/server.properties"
    cp -a "$ROOT/bukkit.yml" "$probe/bukkit.yml"
    cp -a "$ROOT/commands.yml" "$probe/commands.yml"
    cp -a "$ROOT/spigot.yml" "$probe/spigot.yml"
    cp -a "$ROOT/config" "$probe/config"

    [ -f "$ROOT/eula.txt" ] || return 1
    cp "$ROOT/eula.txt" "$probe/eula.txt"
    return 0
}

validate_backup() {
    local backup="$1"
    local stamp
    local probe="$RUN/probe"

    report "validating backup: $(basename "$backup")"
    prepare_probe "$backup" || {
        report "backup validation setup failed"
        return 1
    }

    stamp="$(date +%Y%m%d-%H%M%S)"
    PROBE_LOG="$LOG_DIR/backup-test-$stamp.log"
    start_process probe "$probe" "$PROBE_LOG" "$RUN/probe.stdin" PROBE_FD

    if ! wait_for_log "$PROBE_LOG" 'Done \(' "$START_TIMEOUT"; then
        report "backup failed startup validation"
        cleanup_probe
        rm -rf -- "$probe"
        return 1
    fi

    if ! command_ok "$PROBE_PID" "$PROBE_FD" "$PROBE_LOG" 'There are [0-9]+ of a max of [0-9]+ players online' "$COMMAND_TIMEOUT"; then
        report "backup failed command validation"
        cleanup_probe
        rm -rf -- "$probe"
        return 1
    fi

    cleanup_probe
    rm -rf -- "$probe"
    report "backup validated healthy"
    return 0
}

create_backup() {
    local date stamp candidate

    date="$(date +%F)"
    stamp="$(date +%Y%m%d-%H%M%S)"
    candidate="$BACKUPS/.candidate-$stamp"

    [ -d "$WORLD" ] || {
        report "cannot create backup: world directory is missing"
        return 1
    }

    report "flushing world to disk"
    local offset
    offset=$(wc -c < "$SERVER_LOG")
    printf '%s\n' "save-all flush" >&"$SERVER_FD" || return 1
    if ! for _ in $(seq 1 30); do
        tail -c +$((offset + 1)) "$SERVER_LOG" 2>/dev/null | grep -q 'Saved the game' && break
        sleep 1
    done; then
        report "world save confirmation failed"
        return 1
    fi

    stop_server

    rm -rf -- "$candidate"
    mkdir -p "$candidate"
    report "copying world snapshot"
    cp -a "$WORLD" "$candidate/world" || {
        rm -rf -- "$candidate"
        report "world snapshot copy failed"
        return 1
    }

    if ! validate_backup "$candidate"; then
        rm -rf -- "$candidate"
        return 1
    fi

    printf '%s\n' "created=$date" > "$candidate/metadata"
    printf '%s\n' "verified=$(date -Is)" >> "$candidate/metadata"
    : > "$candidate/HEALTHY"
    mv "$candidate" "$BACKUPS/$stamp"
    printf '%s\n' "$date" > "$RUN/last-backup-date"
    prune_backups
    report "backup promoted healthy: $stamp"
    return 0
}

backup_due() {
    local today last hour
    today="$(date +%F)"
    last=""
    [ -f "$RUN/last-backup-date" ] && last=$(cat "$RUN/last-backup-date")
    hour="$(date +%H)"
    [ "$hour" -ge "$BACKUP_HOUR" ] && [ "$last" != "$today" ]
}

quarantine_world() {
    local dir="$QUARANTINE/world-$(date +%Y%m%d-%H%M%S)"
    mkdir -p "$QUARANTINE"
    mv "$WORLD" "$dir"
    printf '%s\n' "$dir"
}

restore_backup() {
    local backup="$1"
    local quarantine

    report "quarantining failed world"
    quarantine=$(quarantine_world) || return 1

    report "restoring backup: $(basename "$backup")"
    cp -a "$backup/world" "$WORLD" || {
        rm -rf -- "$WORLD"
        mv "$quarantine" "$WORLD"
        return 1
    }

    if start_server; then
        report "restored world passed health verification"
        rm -rf -- "$quarantine"
        return 0
    fi

    report "restored world failed health verification"
    rm -rf -- "$WORLD"
    mv "$quarantine" "$WORLD"
    return 1
}

recover() {
    local backup

    report "entering recovery"
    stop_server

    while IFS= read -r backup; do
        [ -z "$backup" ] && continue
        if restore_backup "$backup"; then
            report "recovery successful"
            return 0
        fi
    done < <(healthy_backup_dirs)

    report "recovery failed: no healthy backup could be started"
    return 1
}

monitor() {
    while true; do
        sleep "$MONITOR_INTERVAL"

        if ! kill -0 "$SERVER_PID" 2>/dev/null; then
            report "Minecraft exited unexpectedly"
            cleanup_server
            return 1
        fi

        if ! command_ok "$SERVER_PID" "$SERVER_FD" "$SERVER_LOG" 'There are [0-9]+ of a max of [0-9]+ players online' "$COMMAND_TIMEOUT"; then
            report "hang detected: Minecraft did not answer a main-thread command"
            cleanup_server
            return 1
        fi

        if backup_due; then
            report "daily backup due"
            if ! create_backup; then
                report "daily backup failed; continuing without promoting a backup"
            fi
            if ! start_server; then
                report "server failed after backup cycle"
                return 1
            fi
        fi
    done
}

main() {
    ensure_dirs
    [ -f "$JAR" ] || { report "missing Paper jar: $JAR"; exit 1; }
    [ -f "$ROOT/eula.txt" ] || { report "missing eula.txt"; exit 1; }
    [ -d "$WORLD" ] || { report "missing world directory: $WORLD"; exit 1; }

    while true; do
        if ! start_server; then
            if ! recover; then
                exit 1
            fi
            continue
        fi

        if ! monitor; then
            if ! recover; then
                exit 1
            fi
        fi
    done
}

main "$@"
