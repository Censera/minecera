package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	root          = "/home/censera/minecraft"
	startScript   = root + "/start.sh"
	world         = root + "/world"
	backups       = root + "/backups"
	quarantine    = root + "/run/quarantine"
	logs          = root + "/logs"
	startTimeout  = 180 * time.Second
	stopTimeout   = 120 * time.Second
	probeTimeout  = 180 * time.Second
	maxBackups    = 7
	maxQuarantine = 30
)

type Supervisor struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	desired bool
}

func logf(format string, args ...any) {
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
	fmt.Print(line)
	_ = os.MkdirAll(logs, 0755)
	f, err := os.OpenFile(filepath.Join(logs, "supervisor.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil { _, _ = f.WriteString(line); _ = f.Close() }
}

func (s *Supervisor) running() bool {
	s.mu.Lock(); defer s.mu.Unlock()
	return s.cmd != nil && s.cmd.Process != nil
}

func (s *Supervisor) start(ctx context.Context) error {
	s.mu.Lock()
	if s.cmd != nil && s.cmd.Process != nil { s.mu.Unlock(); return nil }
	cmd := exec.CommandContext(ctx, "bash", startScript)
	cmd.Dir = root
	stdin, err := cmd.StdinPipe()
	if err != nil { s.mu.Unlock(); return err }
	logPath := filepath.Join(logs, "server-"+time.Now().Format("20060102-150405")+".log")
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil { _ = stdin.Close(); s.mu.Unlock(); return err }
	cmd.Stdout = lf
	cmd.Stderr = lf
	if err = cmd.Start(); err != nil { _ = lf.Close(); _ = stdin.Close(); s.mu.Unlock(); return err }
	s.cmd, s.stdin = cmd, stdin
	s.mu.Unlock()

	logf("starting Minecraft")
	done := make(chan error, 1)
	go func() { err := cmd.Wait(); _ = lf.Close(); done <- err }()
	select {
	case err := <-done:
		s.mu.Lock(); s.cmd, s.stdin = nil, nil; s.mu.Unlock()
		if err != nil { return fmt.Errorf("minecraft exited: %w", err) }
		return fmt.Errorf("minecraft exited")
	case <-time.After(startTimeout):
		_ = s.stop()
		return fmt.Errorf("startup timeout")
	case <-ctx.Done():
		_ = s.stop()
		return ctx.Err()
	}
}

func (s *Supervisor) command(command string) error {
	s.mu.Lock(); defer s.mu.Unlock()
	if s.stdin == nil || s.cmd == nil || s.cmd.Process == nil { return fmt.Errorf("server is not running") }
	_, err := fmt.Fprintln(s.stdin, command)
	return err
}

func (s *Supervisor) stop() error {
	s.mu.Lock()
	cmd, stdin := s.cmd, s.stdin
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil { return nil }
	logf("stopping Minecraft")
	if stdin != nil { _, _ = fmt.Fprintln(stdin, "stop") }
	t := time.NewTimer(stopTimeout)
	defer t.Stop()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		s.mu.Lock(); s.cmd, s.stdin = nil, nil; s.mu.Unlock()
		logf("Minecraft stopped")
		return err
	case <-t.C:
		_ = cmd.Process.Kill()
		s.mu.Lock(); s.cmd, s.stdin = nil, nil; s.mu.Unlock()
		logf("graceful stop timed out; forced Minecraft down")
		return fmt.Errorf("stop timeout")
	}
}

func healthyBackups() []string {
	ents, _ := os.ReadDir(backups)
	var out []string
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") { continue }
		if _, err := os.Stat(filepath.Join(backups, e.Name(), "HEALTHY")); err == nil { out = append(out, filepath.Join(backups, e.Name())) }
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

func validateWorldDir(dir string) error {
	for _, required := range []string{"level.dat", "dimensions"} {
		if _, err := os.Stat(filepath.Join(dir, required)); err != nil { return fmt.Errorf("missing %s", required) }
	}
	return nil
}

func prepareProbe(backup, probe string) error {
	_ = os.RemoveAll(probe)
	if err := os.MkdirAll(probe, 0755); err != nil { return err }
	for _, name := range []string{"plugins", "config", "bukkit.yml", "commands.yml", "spigot.yml", "server.properties", "paper-26.2.jar"} {
		src := filepath.Join(root, name)
		if _, err := os.Stat(src); err != nil { if name == "config" { continue }; return err }
		if err := copyPath(src, filepath.Join(probe, name)); err != nil { return err }
	}
	// Validation must never inherit the live EULA state.
	if err := os.WriteFile(filepath.Join(probe, "eula.txt"), []byte("eula=true\n"), 0644); err != nil { return err }
	return copyPath(filepath.Join(backup, "world"), filepath.Join(probe, "world"))
}

func copyPath(src, dst string) error {
	info, err := os.Stat(src); if err != nil { return err }
	if info.IsDir() {
		return filepath.Walk(src, func(path string, i os.FileInfo, err error) error {
			if err != nil { return err }
			rel, _ := filepath.Rel(src, path); target := filepath.Join(dst, rel)
			if i.IsDir() { return os.MkdirAll(target, i.Mode()) }
			in, err := os.Open(path); if err != nil { return err }; defer in.Close()
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, i.Mode()); if err != nil { return err }; defer out.Close()
			_, err = io.Copy(out, in); return err
		})
	}
	in, err := os.Open(src); if err != nil { return err }; defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode()); if err != nil { return err }; defer out.Close()
	_, err = io.Copy(out, in); return err
}

func validateBackup(backup string) error {
	if err := validateWorldDir(filepath.Join(backup, "world")); err != nil { return err }
	probe := filepath.Join(root, "run", "probe")
	if err := prepareProbe(backup, probe); err != nil { return err }
	defer os.RemoveAll(probe)

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", startScript)
	cmd.Dir = probe
	cmd.Env = append(os.Environ(), "MINECERA_ROOT="+probe, "MINECERA_JAR="+filepath.Join(probe, "paper-26.2.jar"), "MINECERA_XMS=512M", "MINECERA_XMX=1G")
	in, err := cmd.StdinPipe(); if err != nil { return err }
	log, err := os.OpenFile(filepath.Join(logs, "backup-test-"+time.Now().Format("20060102-150405")+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil { return err }
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil { _ = log.Close(); return err }
	_, _ = fmt.Fprintln(in, "list")
	_ = in.Close()
	err = cmd.Wait()
	_ = log.Close()
	if ctx.Err() != nil { return fmt.Errorf("probe timeout") }
	if err != nil { return fmt.Errorf("probe failed: %w", err) }
	return nil
}

func quarantineWorld() (string, error) {
	if _, err := os.Stat(world); err != nil { return "", err }
	name := filepath.Join(quarantine, "world-before-recovery-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(quarantine, 0755); err != nil { return "", err }
	if err := os.Rename(world, name); err != nil { return "", err }
	return name, nil
}

func restoreWorld(backup string) error {
	if err := validateWorldDir(filepath.Join(backup, "world")); err != nil { return err }
	old, err := quarantineWorld(); if err != nil { return err }
	if err := copyPath(filepath.Join(backup, "world"), world); err != nil {
		_ = os.RemoveAll(world); _ = os.Rename(old, world); return err
	}
	logf("restoring backup: %s", filepath.Base(backup))
	return nil
}

func pruneQuarantine() {
	ents, _ := os.ReadDir(quarantine)
	var dirs []string
	for _, e := range ents { if e.IsDir() { dirs = append(dirs, filepath.Join(quarantine, e.Name())) } }
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, d := range dirs[maxQuarantine:] { _ = os.RemoveAll(d) }
}

func main() {
	_ = os.MkdirAll(quarantine, 0755)
	_ = os.MkdirAll(logs, 0755)
	pruneQuarantine()

	s := &Supervisor{desired: true}
	logf("Minecera supervisor started")
	for s.desired {
		ctx := context.Background()
		if err := s.start(ctx); err == nil { continue }
		logf("startup failed: %v", err)
		if !s.desired { break }
		// Never destroy the current world merely because startup failed.
		// Recovery is deliberately conservative: only a known-good backup is eligible.
		for _, backup := range healthyBackups() {
			if err := validateBackup(backup); err != nil { logf("backup rejected: %s: %v", filepath.Base(backup), err); continue }
			if err := restoreWorld(backup); err != nil { logf("restore failed: %v", err); continue }
			if err := s.start(ctx); err == nil {
				logf("recovery successful: %s", filepath.Base(backup))
				// The displaced world is retained. It is evidence, not garbage.
				break
			}
			logf("restored world failed startup; returning to preserved world")
			_ = os.RemoveAll(world)
			// Find the newest quarantine entry and put it back.
			ents, _ := os.ReadDir(quarantine)
			if len(ents) > 0 { sort.Slice(ents, func(i,j int) bool { return ents[i].Name() > ents[j].Name() }); _ = os.Rename(filepath.Join(quarantine, ents[0].Name()), world) }
		}
		time.Sleep(5 * time.Second)
	}
}

// Keep bufio linked into the binary so future line-oriented console handling can
// be added without changing the supervisor's dependency surface.
var _ = bufio.ErrInvalidUnreadByte
