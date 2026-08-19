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
POLL_INTERVAL=0.2
BACKUP_HOUR=04
KEEP_BACKUPS=3
PROBE_XMS=512M
PROBE_XMX=1G

STATE_RUNNING=running
STATE_STOPPED=stopped
DESIRED_STATE="$STATE_RUNNING"
RECOVERY_ALLOWED=1
SERVER_PID=""
SERVER_FD=""
SERVER_FIFO=""
SERVER_LOG=""
SERVER_HEALTH_LOG=""
CONTROL_FD=""
SHUTTING_DOWN=0

report() {
    printf '[%s] %s\n' "$(date '+%F %T')" "$*" | tee -a "$LOG_DIR/supervisor.log"
    logger -t minecera -- "$*" 2>/dev/null || true
}

close_fd() {
    local fd="$1"
    eval "exec ${fd}>&-" 2>/dev/null || true
}

cleanup_server() {
    if [ -n "$SERVER_FD" ]; then
        close_fd "$SERVER_FD"
        SERVER_FD=""
    fi
    if [ -n "$SERVER_FIFO" ]; then
        rm -f "$SERVER_FIFO"
        SERVER_FIFO=""
    fi
    SERVER_PID=""
    SERVER_LOG=""
    SERVER_HEALTH_LOG=""
}

force_stop_server() {
    local pid="$SERVER_PID"
    [ -n "$pid" ] || { cleanup_server; return 0; }

    if kill -0 "$pid" 2>/dev/null; then
        kill -KILL "$pid" 2>/dev/null || true
        for _ in $(seq 1 50); do
            kill -0 "$pid" 2>/dev/null || break
            sleep 0.1
        done
    fi
    cleanup_server
}

stop_server() {
    local pid="$SERVER_PID"
    [ -n "$pid" ] || return 0

    if ! kill -0 "$pid" 2>/dev/null; then
        cleanup_server
        return 0
    fi

    report "stopping Minecraft"
    printf '%s\n' "stop" >&"$SERVER_FD" 2>/dev/null || true

    for _ in $(seq 1 "$STOP_TIMEOUT"); do
        if ! kill -0 "$pid" 2>/dev/null; then
            cleanup_server
            report "Minecraft stopped"
            return 0
        fi
        sleep 1
    done

    report "graceful stop timed out; forcing Minecraft down"
    force_stop_server
    report "Minecraft stopped"
}

shutdown() {
    [ "$SHUTTING_DOWN" -eq 1 ] && return
    SHUTTING_DOWN=1
    DESIRED_STATE="$STATE_STOPPED"
    report "shutdown requested"
    stop_server
    [ -n "$CONTROL_FD" ] && close_fd "$CONTROL_FD"
    rm -f "$CONTROL"
    exit 0
}

trap shutdown INT TERM

ensure_dirs() {
    mkdir -p "$BACKUPS" "$QUARANTINE" "$LOG_DIR" "$RUN"
    rm -f "$CONTROL"
    mkfifo -m 660 "$CONTROL"
    exec {CONTROL_FD}<>"$CONTROL"
}

filter_server_output() {
    local log="$1"
    local health="$2"
    local line

    while IFS= read -r line; do
        if [[ "$line" =~ ^\[[0-9]{2}:[0-9]{2}:[0-9]{2}\ [A-Z]+\]:\ There\ are\ [0-9]+\ of\ a\ max\ of\ [0-9]+\ players\ online: ]]; then
            printf '%s\n' "$line" >> "$health"
        else
            printf '%s\n' "$line" >> "$log"
        fi
    done
}

wait_for_log() {
    local pid="$1" log="$2" pattern="$3" timeout="$4"
    for _ in $(seq 1 "$timeout"); do
        grep -Eq "$pattern" "$log" 2>/dev/null && return 0
        kill -0 "$pid" 2>/dev/null || return 1
        sleep 1
    done
    return 1
}

health_check() {
    local pid="$1" fd="$2" health_log="$3" offset
    offset=$(wc -c < "$health_log" 2>/dev/null || printf '0')
    printf '%s\n' "list" >&"$fd" 2>/dev/null || return 1

    for _ in $(seq 1 "$COMMAND_TIMEOUT"); do
        tail -c +$((offset + 1)) "$health_log" 2>/dev/null |
            grep -Eq 'There are [0-9]+ of a max of [0-9]+ players online:' && return 0
        kill -0 "$pid" 2>/dev/null || return 1
        sleep 1
    done
    return 1
}

start_server() {
    local stamp fifo fd

    if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
        report "start rejected: Minecraft is already running"
        return 1
    fi

    cleanup_server
    stamp="$(date +%Y%m%d-%H%M%S)"
    SERVER_LOG="$LOG_DIR/server-$stamp.log"
    SERVER_HEALTH_LOG="$RUN/server.health"
    fifo="$RUN/server.stdin"
    rm -f "$fifo"
    mkfifo -m 660 "$fifo"
    exec {fd}<>"$fifo"
    : > "$SERVER_HEALTH_LOG"

    report "starting Minecraft"
    bash "$START" <&"$fd" > >(filter_server_output "$SERVER_LOG" "$SERVER_HEALTH_LOG") 2>&1 &
    SERVER_PID=$!
    SERVER_FD="$fd"
    SERVER_FIFO="$fifo"

    if ! wait_for_log "$SERVER_PID" "$SERVER_LOG" 'Done \(' "$START_TIMEOUT"; then
        report "startup failed"
        force_stop_server
        return 1
    fi

    if ! health_check "$SERVER_PID" "$SERVER_FD" "$SERVER_HEALTH_LOG"; then
        report "startup health check failed"
        force_stop_server
        return 1
    fi

    report "Minecraft healthy"
    return 0
}

send_command() {
    [ -n "$SERVER_PID" ] || return 1
    kill -0 "$SERVER_PID" 2>/dev/null || return 1
    printf '%s\n' "$1" >&"$SERVER_FD" 2>/dev/null
}

healthy_backup_dirs() {
    find "$BACKUPS" -mindepth 2 -maxdepth 2 -type f -name HEALTHY -printf '%h\n' 2>/dev/null | sort -r
}

prune_backups() {
    local count=0 dir
    while IFS= read -r dir; do
        [ -n "$dir" ] || continue
        count=$((count + 1))
        if [ "$count" -gt "$KEEP_BACKUPS" ]; then
            report "removing old backup: $(basename "$dir")"
            rm -rf -- "$dir"
        fi
    done < <(healthy_backup_dirs)
}

prepare_probe() {
    local backup="$1" probe="$RUN/probe"

    rm -rf -- "$probe"
    mkdir -p "$probe"
    cp -a "$ROOT/plugins" "$probe/plugins" || return 1
    if [ -d "$ROOT/config" ]; then
        cp -a "$ROOT/config" "$probe/config" || return 1
    fi
    cp -a "$ROOT/bukkit.yml" "$ROOT/commands.yml" "$ROOT/spigot.yml" "$ROOT/server.properties" "$ROOT/eula.txt" "$probe/" || return 1
    cp "$JAR" "$probe/paper-26.2.jar" || return 1
    cp -a "$backup/world" "$probe/world" || return 1
}

validate_backup() {
    local backup="$1"
    local stamp probe fifo fd health_log pid log

    report "validating backup: $(basename "$backup")"
    prepare_probe "$backup" || {
        report "backup validation setup failed"
        return 1
    }

    stamp="$(date +%Y%m%d-%H%M%S)"
    probe="$RUN/probe"
    log="$LOG_DIR/backup-test-$stamp.log"
    fifo="$RUN/probe.stdin"
    health_log="$RUN/probe.health"

    rm -f "$fifo"
    mkfifo -m 660 "$fifo"
    exec {fd}<>"$fifo"
    : > "$health_log"

    MINECERA_ROOT="$probe" \
    MINECERA_JAR="$probe/paper-26.2.jar" \
    MINECERA_XMS="$PROBE_XMS" \
    MINECERA_XMX="$PROBE_XMX" \
    bash "$START" <&"$fd" > >(filter_server_output "$log" "$health_log") 2>&1 &
    pid=$!

    if ! wait_for_log "$pid" "$log" 'Done \(' "$START_TIMEOUT"; then
        report "backup failed startup validation"
        kill -KILL "$pid" 2>/dev/null || true
        close_fd "$fd"
        rm -f "$fifo"
        rm -rf -- "$probe"
        return 1
    fi

    if ! health_check "$pid" "$fd" "$health_log"; then
        report "backup failed command validation"
        kill -KILL "$pid" 2>/dev/null || true
        close_fd "$fd"
        rm -f "$fifo"
        rm -rf -- "$probe"
        return 1
    fi

    printf '%s\n' "stop" >&"$fd" 2>/dev/null || true
    for _ in $(seq 1 60); do
        kill -0 "$pid" 2>/dev/null || break
        sleep 1
    done
    kill -KILL "$pid" 2>/dev/null || true
    close_fd "$fd"
    rm -f "$fifo"
    rm -rf -- "$probe"
    report "backup validated healthy"
    return 0
}

create_backup() {
    local date stamp candidate offset saved

    [ "$DESIRED_STATE" = "$STATE_RUNNING" ] || {
        report "backup rejected: server is stopped"
        return 1
    }
    [ -d "$WORLD" ] || {
        report "cannot create backup: world directory is missing"
        return 1
    }
    [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null || {
        report "backup rejected: server is not running"
        return 1
    }

    date="$(date +%F)"
    stamp="$(date +%Y%m%d-%H%M%S)"
    candidate="$BACKUPS/.candidate-$stamp"

    report "flushing world to disk"
    offset=$(wc -c < "$SERVER_LOG" 2>/dev/null || printf '0')
    send_command "save-all flush" || return 1
    saved=0
    for _ in $(seq 1 30); do
        if tail -c +$((offset + 1)) "$SERVER_LOG" 2>/dev/null | grep -q 'Saved the game'; then
            saved=1
            break
        fi
        sleep 1
    done
    [ "$saved" -eq 1 ] || {
        report "world save confirmation failed"
        return 1
    }

    stop_server

    rm -rf -- "$candidate"
    mkdir -p "$candidate"
    report "copying world snapshot"
    if ! cp -a "$WORLD" "$candidate/world"; then
        rm -rf -- "$candidate"
        report "world snapshot copy failed"
        start_server || report "server failed to restart after backup copy failure"
        return 1
    fi

    if ! validate_backup "$candidate"; then
        rm -rf -- "$candidate"
        report "backup validation failed; candidate discarded"
        start_server || report "server failed to restart after backup validation failure"
        return 1
    fi

    printf '%s\n' "created=$date" > "$candidate/metadata"
    printf '%s\n' "verified=$(date -Is)" >> "$candidate/metadata"
    : > "$candidate/HEALTHY"
    mv "$candidate" "$BACKUPS/$stamp"
    printf '%s\n' "$date" > "$RUN/last-backup-date"
    prune_backups
    report "backup promoted healthy: $stamp"

    if ! start_server; then
        report "backup succeeded but Minecraft failed to restart"
    fi
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
    local backup="$1" quarantine

    quarantine=$(quarantine_world) || return 1
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
    force_stop_server
    rm -rf -- "$WORLD"
    mv "$quarantine" "$WORLD"
    return 1
}

recover() {
    local backup

    report "entering recovery"
    force_stop_server

    while IFS= read -r backup; do
        [ -n "$backup" ] || continue
        if restore_backup "$backup"; then
            report "recovery successful"
            return 0
        fi
    done < <(healthy_backup_dirs)

    report "recovery failed: no healthy backup could be started"
    return 1
}

handle_command() {
    local command="$1"

    case "$command" in
        status)
            if [ "$DESIRED_STATE" = "$STATE_STOPPED" ]; then
                printf 'stopped\n'
            elif [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
                printf 'running pid=%s\n' "$SERVER_PID"
            else
                printf 'starting\n'
            fi
            ;;
        start)
            DESIRED_STATE="$STATE_RUNNING"
            RECOVERY_ALLOWED=1
            if [ -z "$SERVER_PID" ] || ! kill -0 "$SERVER_PID" 2>/dev/null; then
                start_server || report "console start failed"
            else
                report "console start ignored: server is already running"
            fi
            ;;
        stop)
            DESIRED_STATE="$STATE_STOPPED"
            report "console requested stop"
            stop_server
            ;;
        restart)
            DESIRED_STATE="$STATE_RUNNING"
            RECOVERY_ALLOWED=0
            report "console requested restart"
            stop_server
            if start_server; then
                report "Minecraft restarted without backup restore"
            else
                report "console restart failed; preserving current world and retrying"
            fi
            ;;
        backup)
            DESIRED_STATE="$STATE_RUNNING"
            if ! create_backup; then
                report "console backup failed"
            fi
            ;;
        save)
            send_command "save-all flush" || report "console save failed: server is not running"
            ;;
        reload)
            send_command "reload confirm" || report "console reload failed: server is not running"
            ;;
        "")
            ;;
        *)
            send_command "$command" || report "console command rejected: server is not running"
            ;;
    esac
}

poll_control() {
    local command
    while IFS= read -r -t 0.01 command <&"$CONTROL_FD"; do
        handle_command "$command"
    done
}

monitor_server() {
    local elapsed=0

    while [ "$DESIRED_STATE" = "$STATE_RUNNING" ]; do
        poll_control
        [ "$DESIRED_STATE" = "$STATE_RUNNING" ] || return 0

        if [ -z "$SERVER_PID" ]; then
            return 1
        fi
        if ! kill -0 "$SERVER_PID" 2>/dev/null; then
            report "Minecraft exited unexpectedly"
            cleanup_server
            return 1
        fi

        elapsed=$((elapsed + 1))
        if [ "$elapsed" -ge "$MONITOR_INTERVAL" ]; then
            elapsed=0
            if ! health_check "$SERVER_PID" "$SERVER_FD" "$SERVER_HEALTH_LOG"; then
                report "hang detected: Minecraft did not answer a main-thread command"
                force_stop_server
                return 1
            fi

            if backup_due; then
                report "daily backup due"
                create_backup || report "daily backup failed"
            fi
        fi
        sleep "$POLL_INTERVAL"
    done
    return 0
}

main() {
    ensure_dirs
    [ -f "$JAR" ] || { report "missing Paper jar"; exit 1; }
    [ -f "$ROOT/eula.txt" ] || { report "missing eula.txt"; exit 1; }
    [ -d "$WORLD" ] || { report "missing world directory"; exit 1; }

    while true; do
        poll_control

        if [ "$DESIRED_STATE" = "$STATE_STOPPED" ]; then
            sleep "$POLL_INTERVAL"
            continue
        fi

        if [ -z "$SERVER_PID" ] || ! kill -0 "$SERVER_PID" 2>/dev/null; then
            if ! start_server; then
                if [ "$RECOVERY_ALLOWED" -eq 1 ]; then
                    recover || exit 1
                else
                    report "Minecraft failed to start; current world preserved"
                fi
            fi
            RECOVERY_ALLOWED=1
            continue
        fi

        monitor_server
        case "$?" in
            0) ;;
            1)
                [ "$DESIRED_STATE" = "$STATE_RUNNING" ] || continue
                if [ "$RECOVERY_ALLOWED" -eq 1 ]; then
                    recover || exit 1
                else
                    report "unexpected exit during restart cycle; current world preserved"
                    RECOVERY_ALLOWED=1
                fi
                ;;
        esac
    done
}

main "$@"
