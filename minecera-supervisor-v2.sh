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
CONTROL="$RUN/control"

START_TIMEOUT=180
STOP_TIMEOUT=120
COMMAND_TIMEOUT=15
MONITOR_INTERVAL=30
BACKUP_HOUR=04
KEEP_BACKUPS=3

SERVER_PID=""
SERVER_FD=""
SERVER_FIFO=""
SERVER_LOG=""
SERVER_HEALTH_LOG=""
SHUTTING_DOWN=0

report() {
    printf '[%s] %s\n' "$(date '+%F %T')" "$*" | tee -a "$LOG_DIR/supervisor.log"
    logger -t minecera -- "$*" 2>/dev/null || true
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
    fi
    [ -n "$SERVER_FIFO" ] && rm -f "$SERVER_FIFO"
    SERVER_PID=""
    SERVER_FD=""
    SERVER_FIFO=""
    SERVER_LOG=""
    SERVER_HEALTH_LOG=""
}

shutdown() {
    [ "$SHUTTING_DOWN" -eq 1 ] && return
    SHUTTING_DOWN=1
    report "shutdown requested"
    cleanup_server
    rm -f "$CONTROL"
    exit 0
}

trap shutdown INT TERM

ensure_dirs() {
    mkdir -p "$BACKUPS" "$QUARANTINE" "$LOG_DIR" "$RUN"
    rm -f "$CONTROL"
    mkfifo -m 660 "$CONTROL"
}

filter_output() {
    local log="$1"
    local health="$2"
    while IFS= read -r line; do
        if [[ "$line" =~ There\ are\ [0-9]+\ of\ a\ max\ of\ [0-9]+\ players\ online: ]]; then
            printf '%s\n' "$line" >> "$health"
        else
            printf '%s\n' "$line" >> "$log"
        fi
    done
}

start_server() {
    local stamp fifo fd
    stamp="$(date +%Y%m%d-%H%M%S)"
    fifo="$RUN/server.stdin"
    SERVER_LOG="$LOG_DIR/server-$stamp.log"
    SERVER_HEALTH_LOG="$RUN/server.health"
    : > "$SERVER_HEALTH_LOG"
    rm -f "$fifo"
    mkfifo -m 660 "$fifo"
    exec {fd}<>"$fifo"
    report "starting Minecraft"
    bash "$START" <&"$fd" > >(filter_output "$SERVER_LOG" "$SERVER_HEALTH_LOG") 2>&1 &
    SERVER_PID=$!
    SERVER_FD="$fd"
    SERVER_FIFO="$fifo"

    for _ in $(seq 1 "$START_TIMEOUT"); do
        grep -Eq 'Done \(' "$SERVER_LOG" 2>/dev/null && break
        kill -0 "$SERVER_PID" 2>/dev/null || return 1
        sleep 1
    done
    grep -Eq 'Done \(' "$SERVER_LOG" 2>/dev/null || return 1

    if ! health_check; then
        report "startup health check failed"
        return 1
    fi
    report "Minecraft healthy"
    return 0
}

health_check() {
    local offset
    offset=$(wc -c < "$SERVER_HEALTH_LOG" 2>/dev/null || printf '0')
    printf '%s\n' "list" >&"$SERVER_FD" 2>/dev/null || return 1
    for _ in $(seq 1 "$COMMAND_TIMEOUT"); do
        tail -c +$((offset + 1)) "$SERVER_HEALTH_LOG" 2>/dev/null |
            grep -Eq 'There are [0-9]+ of a max of [0-9]+ players online:' && return 0
        kill -0 "$SERVER_PID" 2>/dev/null || return 1
        sleep 1
    done
    return 1
}

stop_server() {
    [ -z "$SERVER_PID" ] && return 0
    report "stopping Minecraft"
    printf '%s\n' "stop" >&"$SERVER_FD" 2>/dev/null || true
    for _ in $(seq 1 "$STOP_TIMEOUT"); do
        kill -0 "$SERVER_PID" 2>/dev/null || { cleanup_server; return 0; }
        sleep 1
    done
    report "graceful stop timed out; forcing Minecraft down"
    kill -KILL "$SERVER_PID" 2>/dev/null || true
    cleanup_server
}

healthy_backups() {
    find "$BACKUPS" -mindepth 2 -maxdepth 2 -type f -name HEALTHY -printf '%h\n' 2>/dev/null | sort -r
}

prune_backups() {
    local count=0 dir
    while IFS= read -r dir; do
        [ -z "$dir" ] && continue
        count=$((count + 1))
        [ "$count" -le "$KEEP_BACKUPS" ] && continue
        report "removing old backup: $(basename "$dir")"
        rm -rf -- "$dir"
    done < <(healthy_backups)
}

validate_backup() {
    local backup="$1" probe="$RUN/probe" fifo fd stamp log health
    rm -rf -- "$probe"
    mkdir -p "$probe"
    cp -a "$ROOT/plugins" "$ROOT/config" "$probe/"
    cp -a "$ROOT/bukkit.yml" "$ROOT/commands.yml" "$ROOT/spigot.yml" "$ROOT/server.properties" "$ROOT/eula.txt" "$probe/"
    cp "$JAR" "$probe/paper-26.2.jar"
    cp -a "$backup/world" "$probe/world"
    fifo="$RUN/probe.stdin"
    stamp="$(date +%Y%m%d-%H%M%S)"
    log="$LOG_DIR/backup-test-$stamp.log"
    health="$RUN/probe.health"
    : > "$health"
    rm -f "$fifo"
    mkfifo -m 660 "$fifo"
    exec {fd}<>"$fifo"
    report "validating backup: $(basename "$backup")"
    MINECERA_ROOT="$probe" MINECERA_JAR="$probe/paper-26.2.jar" bash "$START" <&"$fd" > >(filter_output "$log" "$health") 2>&1 &
    local pid=$!
    for _ in $(seq 1 "$START_TIMEOUT"); do
        grep -Eq 'Done \(' "$log" 2>/dev/null && break
        kill -0 "$pid" 2>/dev/null || { kill -KILL "$pid" 2>/dev/null || true; return 1; }
        sleep 1
    done
    if ! grep -Eq 'Done \(' "$log" 2>/dev/null; then kill -KILL "$pid" 2>/dev/null || true; return 1; fi
    local offset=0
    offset=$(wc -c < "$health" 2>/dev/null || printf '0')
    printf '%s\n' "list" >&"$fd" || { kill -KILL "$pid" 2>/dev/null || true; return 1; }
    for _ in $(seq 1 "$COMMAND_TIMEOUT"); do
        tail -c +$((offset + 1)) "$health" 2>/dev/null | grep -Eq 'There are [0-9]+ of a max of [0-9]+ players online:' && {
            kill -INT "$pid" 2>/dev/null || true
            wait "$pid" 2>/dev/null || true
            eval "exec ${fd}>&-" 2>/dev/null || true
            rm -f "$fifo"
            rm -rf -- "$probe"
            report "backup validated healthy"
            return 0
        }
        kill -0 "$pid" 2>/dev/null || break
        sleep 1
    done
    kill -KILL "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    eval "exec ${fd}>&-" 2>/dev/null || true
    rm -f "$fifo"
    rm -rf -- "$probe"
    report "backup failed validation"
    return 1
}

create_backup() {
    local stamp date candidate offset saved=0
    date="$(date +%F)"
    stamp="$(date +%Y%m%d-%H%M%S)"
    candidate="$BACKUPS/.candidate-$stamp"
    [ -n "$SERVER_PID" ] || { report "backup rejected: server is not running"; return 1; }
    report "flushing world to disk"
    offset=$(wc -c < "$SERVER_LOG")
    printf '%s\n' "save-all flush" >&"$SERVER_FD" || return 1
    for _ in $(seq 1 30); do
        tail -c +$((offset + 1)) "$SERVER_LOG" 2>/dev/null | grep -q 'Saved the game' && { saved=1; break; }
        sleep 1
    done
    [ "$saved" -eq 1 ] || { report "world save confirmation failed"; return 1; }
    stop_server
    rm -rf -- "$candidate"
    mkdir -p "$candidate"
    report "copying world snapshot"
    cp -a "$WORLD" "$candidate/world" || { rm -rf -- "$candidate"; return 1; }
    validate_backup "$candidate" || { rm -rf -- "$candidate"; return 1; }
    printf 'created=%s\nverified=%s\n' "$date" "$(date -Is)" > "$candidate/metadata"
    : > "$candidate/HEALTHY"
    mv "$candidate" "$BACKUPS/$stamp"
    printf '%s\n' "$date" > "$RUN/last-backup-date"
    prune_backups
    report "backup promoted healthy: $stamp"
    return 0
}

restore_backup() {
    local backup="$1" quarantine="$QUARANTINE/world-$(date +%Y%m%d-%H%M%S)"
    mkdir -p "$QUARANTINE"
    report "quarantining failed world"
    mv "$WORLD" "$quarantine" || return 1
    report "restoring backup: $(basename "$backup")"
    if ! cp -a "$backup/world" "$WORLD"; then
        rm -rf -- "$WORLD"
        mv "$quarantine" "$WORLD"
        return 1
    fi
    if start_server; then
        report "restored world passed health verification"
        rm -rf -- "$quarantine"
        return 0
    fi
    report "restored world failed health verification"
    cleanup_server
    rm -rf -- "$WORLD"
    mv "$quarantine" "$WORLD"
    return 1
}

recover() {
    local backup
    report "entering recovery"
    while IFS= read -r backup; do
        [ -z "$backup" ] && continue
        restore_backup "$backup" && { report "recovery successful"; return 0; }
    done < <(healthy_backups)
    report "recovery failed: no healthy backup could be started"
    return 1
}

last_backup_today() {
    local today last
    today="$(date +%F)"
    [ -f "$RUN/last-backup-date" ] || return 1
    last=$(cat "$RUN/last-backup-date")
    [ "$last" = "$today" ]
}

handle_request() {
    local command="$1"
    case "$command" in
        status)
            if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then printf 'running pid=%s\n' "$SERVER_PID"; else printf 'stopped\n'; fi
            ;;
        stop)
            report "console requested stop"
            stop_server
            ;;
        start)
            report "console requested start"
            if [ -z "$SERVER_PID" ]; then
                start_server || recover
            else
                report "start ignored: server is already running"
            fi
            ;;
        restart)
            report "console requested restart"
            stop_server
            start_server || recover
            ;;
        backup)
            create_backup || report "console backup failed"
            [ -z "$SERVER_PID" ] && start_server || true
            ;;
        save)
            [ -n "$SERVER_PID" ] && printf '%s\n' "save-all flush" >&"$SERVER_FD" || report "console save failed: server is not running"
            ;;
        reload)
            [ -n "$SERVER_PID" ] && printf '%s\n' "reload confirm" >&"$SERVER_FD" || report "console reload failed: server is not running"
            ;;
        *)
            [ -n "$SERVER_PID" ] && printf '%s\n' "$command" >&"$SERVER_FD" || report "console command rejected: server is not running"
            ;;
    esac
}

main() {
    ensure_dirs
    [ -f "$JAR" ] || { report "missing Paper jar"; exit 1; }
    [ -f "$ROOT/eula.txt" ] || { report "missing eula.txt"; exit 1; }
    [ -d "$WORLD" ] || { report "missing world directory"; exit 1; }

    start_server || recover || exit 1

    local elapsed=0 command
    while true; do
        if IFS= read -r -t 1 command < "$CONTROL"; then
            [ -n "$command" ] && handle_request "$command"
            continue
        fi

        sleep 0.1
        elapsed=$((elapsed + 1))
        [ "$elapsed" -lt "$MONITOR_INTERVAL" ] && continue
        elapsed=0

        if [ -z "$SERVER_PID" ] || ! kill -0 "$SERVER_PID" 2>/dev/null; then
            report "Minecraft exited unexpectedly"
            recover || exit 1
            continue
        fi

        if ! health_check; then
            report "hang detected: Minecraft did not answer a main-thread command"
            cleanup_server
            recover || exit 1
            continue
        fi

        if [ "$(date +%H)" -ge "$BACKUP_HOUR" ] && ! last_backup_today; then
            report "daily backup due"
            create_backup || report "daily backup failed; continuing without promoting a backup"
            [ -z "$SERVER_PID" ] && start_server || true
        fi
    done
}

main "$@"
