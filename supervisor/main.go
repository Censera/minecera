package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	root = "/home/censera/minecraft"
	startScript = root + "/start.sh"
	worldDir = root + "/world"
	backupsDir = root + "/backups"
	runDir = root + "/run"
	logsDir = root + "/logs"
	quarantineDir = runDir + "/quarantine"
	controlFIFO = runDir + "/control"
	serverFIFO = runDir + "/server.stdin"

	startupTimeout = 180 * time.Second
	stopTimeout = 120 * time.Second
	healthTimeout = 15 * time.Second
	worldRetries = 3
	maxBackups = 7
	maxQuarantine = 30
	probeXMS = "512M"
	probeXMX = "1G"
)

type Server struct {
	cmd *exec.Cmd
	stdin io.WriteCloser
	done chan error
	logPath string
	lines chan string
	mu sync.Mutex
}

type Supervisor struct {
	server *Server
	control *os.File
	commandCh chan string
	desired bool
	recoveryAllowed bool
}

func logf(format string, args ...any) {
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
	fmt.Print(line)
	_ = os.MkdirAll(logsDir, 0755)
	f, err := os.OpenFile(filepath.Join(logsDir, "supervisor.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil { _, _ = f.WriteString(line); _ = f.Close() }
}

func main() {
	for _, dir := range []string{runDir, logsDir, backupsDir, quarantineDir} { _ = os.MkdirAll(dir, 0755) }
	if err := ensureFIFO(controlFIFO, 0660); err != nil { panic(err) }
	if err := ensureFIFO(serverFIFO, 0660); err != nil { panic(err) }

	control, err := os.OpenFile(controlFIFO, os.O_RDWR, 0660)
	if err != nil { panic(err) }
	defer control.Close()

	s := &Supervisor{
		control: control,
		commandCh: make(chan string, 32),
		desired: true,
		recoveryAllowed: true,
	}
	go s.readControl()

	logf("Minecera supervisor started")
	for s.desired {
		if err := s.runServer(); err == nil {
			continue
		}
		if !s.desired { break }
		if !s.recoveryAllowed {
			s.recoveryAllowed = true
			continue
		}
		if s.retryCurrentWorld() {
			continue
		}
		if !s.recoverFromBackups() {
			logf("recovery failed; current world preserved and supervisor stopped")
			s.desired = false
		}
	}

	_ = os.Remove(controlFIFO)
	_ = os.Remove(serverFIFO)
	logf("Minecera supervisor stopped")
}

func ensureFIFO(path string, mode uint32) error {
	if info, err := os.Stat(path); err == nil {
		if info.Mode()&os.ModeNamedPipe == 0 { return fmt.Errorf("%s exists and is not a FIFO", path) }
		return nil
	}
	if err := syscall.Mkfifo(path, mode); err != nil && !errors.Is(err, syscall.EEXIST) { return err }
	return nil
}

func (s *Supervisor) readControl() {
	r := bufio.NewReader(s.control)
	for s.desired {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimSpace(line)
			if line != "" { s.commandCh <- line }
		}
		if err != nil {
			if errors.Is(err, io.EOF) { time.Sleep(50 * time.Millisecond); continue }
			return
		}
	}
}

func (s *Supervisor) runServer() error {
	server, err := s.startProcess(root, true)
	if err != nil { return err }
	s.server = server

	if err := s.waitReady(server); err != nil {
		s.stopServer()
		s.server = nil
		return err
	}
	logf("Minecraft healthy")

	for {
		select {
		case command := <-s.commandCh:
			if err := s.handleCommand(command); err != nil { logf("command failed: %v", err) }
			if !s.desired || s.server == nil { return nil }
		case err := <-server.done:
			s.server = nil
			if err != nil { logf("Minecraft exited: %v", err); return err }
			logf("Minecraft stopped")
			return nil
		}
	}
}

func (s *Supervisor) startProcess(dir string, live bool) (*Server, error) {
	if dir != root { _ = os.Remove(filepath.Join(dir, "eula.txt")); if err := os.WriteFile(filepath.Join(dir, "eula.txt"), []byte("eula=true\n"), 0644); err != nil { return nil, err } }

	stamp := time.Now().Format("20060102-150405.000")
	logPath := filepath.Join(logsDir, "server-"+strings.ReplaceAll(stamp, ".", "")+".log")
	if !live { logPath = filepath.Join(logsDir, "backup-test-"+strings.ReplaceAll(stamp, ".", "")+".log") }
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil { return nil, err }

	cmd := exec.Command("bash", startScript)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if dir != root {
		cmd.Env = append(cmd.Env, "MINECERA_ROOT="+dir, "MINECERA_JAR="+filepath.Join(dir, "paper-26.2.jar"), "MINECERA_XMS="+probeXMS, "MINECERA_XMX="+probeXMX)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil { _ = logFile.Close(); return nil, err }
	stdout, err := cmd.StdoutPipe()
	if err != nil { _ = stdin.Close(); _ = logFile.Close(); return nil, err }
	stderr, err := cmd.StderrPipe()
	if err != nil { _ = stdin.Close(); _ = stdout.Close(); _ = logFile.Close(); return nil, err }

	if err := cmd.Start(); err != nil { _ = logFile.Close(); return nil, err }

	server := &Server{cmd: cmd, stdin: stdin, done: make(chan error, 1), logPath: logPath, lines: make(chan string, 256)}
	copyOutput := func(reader io.Reader) {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			_, _ = fmt.Fprintln(logFile, line)
			select { case server.lines <- line: default: }
		}
	}
	go copyOutput(stdout)
	go copyOutput(stderr)
	go func() {
		err := cmd.Wait()
		_ = logFile.Close()
		_ = stdin.Close()
		server.done <- err
	}()

	logf("starting Minecraft")
	return server, nil
}

func (s *Supervisor) waitReady(server *Server) error {
	deadline := time.NewTimer(startupTimeout)
	defer deadline.Stop()
	for {
		select {
		case line := <-server.lines:
			if strings.Contains(line, "Done (") { return s.healthCheck(server) }
		case err := <-server.done:
			return fmt.Errorf("startup failed: %w", err)
		case <-deadline.C:
			return fmt.Errorf("startup timeout")
		case command := <-s.commandCh:
			if command == "stop" { s.desired = false; return fmt.Errorf("startup cancelled") }
		}
	}
}

func (s *Supervisor) healthCheck(server *Server) error {
	if err := writeCommand(server, "list"); err != nil { return err }
	deadline := time.NewTimer(healthTimeout)
	defer deadline.Stop()
	for {
		select {
		case line := <-server.lines:
			if strings.Contains(line, "There are ") && strings.Contains(line, " players online:") { return nil }
		case err := <-server.done:
			return fmt.Errorf("health check failed: %w", err)
		case <-deadline.C:
			return fmt.Errorf("health check timeout")
		case command := <-s.commandCh:
			if command == "stop" { s.desired = false; return fmt.Errorf("health check cancelled") }
		}
	}
}

func writeCommand(server *Server, command string) error {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.stdin == nil || server.cmd == nil || server.cmd.Process == nil { return fmt.Errorf("server is not running") }
	_, err := fmt.Fprintln(server.stdin, command)
	return err
}

func (s *Supervisor) stopServer() error {
	server := s.server
	if server == nil { return nil }
	logf("stopping Minecraft")
	_ = writeCommand(server, "stop")
	select {
	case err := <-server.done:
		s.server = nil
		logf("Minecraft stopped")
		return err
	case <-time.After(stopTimeout):
		_ = server.cmd.Process.Kill()
		_ = server.cmd.Wait()
		s.server = nil
		logf("graceful stop timed out; forced Minecraft down")
		return fmt.Errorf("stop timeout")
	}
}

func (s *Supervisor) handleCommand(command string) error {
	switch command {
	case "status":
		if s.server == nil { fmt.Println("stopped"); return nil }
		fmt.Printf("running pid=%d\n", s.server.cmd.Process.Pid)
	case "start":
		s.desired = true
		s.recoveryAllowed = true
	case "stop":
		s.desired = false
		return s.stopServer()
	case "restart":
		s.desired = true
		s.recoveryAllowed = false
		return s.stopServer()
	case "backup":
		return s.createBackup()
	case "save":
		return writeCommand(s.server, "save-all flush")
	case "reload":
		return writeCommand(s.server, "reload confirm")
	default:
		return writeCommand(s.server, command)
	}
	return nil
}

func (s *Supervisor) retryCurrentWorld() bool {
	for attempt := 1; attempt <= worldRetries; attempt++ {
		logf("retrying current world (%d/%d)", attempt, worldRetries)
		if err := s.runCurrentOnce(); err == nil { return true }
	}
	logf("current world failed %d startup attempts; preserving world", worldRetries)
	return false
}

func (s *Supervisor) runCurrentOnce() error {
	server, err := s.startProcess(root, true)
	if err != nil { return err }
	s.server = server
	if err := s.waitReady(server); err != nil { _ = s.stopServer(); return err }
	logf("Minecraft healthy")
	for {
		select {
		case command := <-s.commandCh:
			if err := s.handleCommand(command); err != nil { logf("command failed: %v", err) }
			if !s.desired || s.server == nil { return nil }
		case err := <-server.done:
			s.server = nil
			if err != nil { return err }
			return nil
		}
	}
}

func healthyBackups() []string {
	ents, _ := os.ReadDir(backupsDir)
	var out []string
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") { continue }
		if _, err := os.Stat(filepath.Join(backupsDir, e.Name(), "HEALTHY")); err == nil { out = append(out, filepath.Join(backupsDir, e.Name())) }
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

func copyTree(src, dst string) error {
	info, err := os.Stat(src); if err != nil { return err }
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil { return err }
	return filepath.Walk(src, func(path string, i os.FileInfo, walkErr error) error {
		if walkErr != nil { return walkErr }
		rel, err := filepath.Rel(src, path); if err != nil { return err }
		target := filepath.Join(dst, rel)
		if i.IsDir() { return os.MkdirAll(target, i.Mode().Perm()) }
		in, err := os.Open(path); if err != nil { return err }
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, i.Mode().Perm()); if err != nil { return err }
		_, copyErr := io.Copy(out, in); closeErr := out.Close(); if copyErr != nil { return copyErr }; return closeErr
	})
}

func (s *Supervisor) prepareProbe(backup, probe string) error {
	_ = os.RemoveAll(probe)
	if err := os.MkdirAll(probe, 0755); err != nil { return err }
	for _, name := range []string{"plugins", "config", "bukkit.yml", "commands.yml", "spigot.yml", "server.properties", "paper-26.2.jar"} {
		src := filepath.Join(root, name)
		if _, err := os.Stat(src); err != nil { if name == "config" { continue }; return err }
		if err := copyTree(src, filepath.Join(probe, name)); err != nil { return err }
	}
	if err := os.WriteFile(filepath.Join(probe, "eula.txt"), []byte("eula=true\n"), 0644); err != nil { return err }
	return copyTree(filepath.Join(backup, "world"), filepath.Join(probe, "world"))
}

func (s *Supervisor) validateBackup(backup string) error {
	if _, err := os.Stat(filepath.Join(backup, "world", "level.dat")); err != nil { return fmt.Errorf("invalid world: %w", err) }
	probe := filepath.Join(runDir, "probe")
	if err := s.prepareProbe(backup, probe); err != nil { return err }
	defer os.RemoveAll(probe)
	server, err := s.startProcess(probe, false)
	if err != nil { return err }
	defer func() { if server.cmd.ProcessState == nil { _ = server.cmd.Process.Kill() } }()
	if err := s.waitReadyProbe(server); err != nil { _ = server.cmd.Process.Kill(); _, _ = server.cmd.Process.Wait(); return err }
	_ = writeCommand(server, "stop")
	select { case <-server.done: return nil; case <-time.After(60 * time.Second): _ = server.cmd.Process.Kill(); _, _ = server.cmd.Process.Wait(); return fmt.Errorf("backup stop timeout") }
}

func (s *Supervisor) waitReadyProbe(server *Server) error {
	deadline := time.NewTimer(probeTimeout)
	defer deadline.Stop()
	for {
		select {
		case line := <-server.lines:
			if strings.Contains(line, "Done (") { return s.healthCheck(server) }
		case err := <-server.done:
			return fmt.Errorf("probe startup failed: %w", err)
		case <-deadline.C:
			return fmt.Errorf("probe timeout")
		}
	}
}

func (s *Supervisor) recoverFromBackups() bool {
	logf("entering automatic recovery")
	for _, backup := range healthyBackups() {
		if err := s.validateBackup(backup); err != nil { logf("backup rejected: %s: %v", filepath.Base(backup), err); continue }
		if err := s.restoreBackup(backup); err != nil { logf("restore rejected: %s: %v", filepath.Base(backup), err); continue }
		if s.runCurrentOnce() == nil {
			logf("automatic recovery successful: %s", filepath.Base(backup))
			return true
		}
		logf("restored world failed after restore; trying next backup")
	}
	return false
}

func (s *Supervisor) restoreBackup(backup string) error {
	name := "world-before-recovery-" + time.Now().Format("20060102-150405")
	old := filepath.Join(quarantineDir, name)
	if err := os.Rename(worldDir, old); err != nil { return fmt.Errorf("quarantine current world: %w", err) }
	if err := copyTree(filepath.Join(backup, "world"), worldDir); err != nil {
		_ = os.RemoveAll(worldDir); _ = os.Rename(old, worldDir); return err
	}
	logf("restoring backup: %s (preserved %s)", filepath.Base(backup), name)
	pruneQuarantine()
	return nil
}

func (s *Supervisor) createBackup() error {
	if s.server == nil { return fmt.Errorf("server is not running") }
	stamp := time.Now().Format("20060102-150405")
	candidate := filepath.Join(backupsDir, ".candidate-"+stamp)
	if err := os.RemoveAll(candidate); err != nil { return err }
	if err := writeCommand(s.server, "save-all flush"); err != nil { return err }
	time.Sleep(2 * time.Second)
	if err := s.stopServer(); err != nil { return err }
	if err := copyTree(worldDir, filepath.Join(candidate, "world")); err != nil { _ = os.RemoveAll(candidate); _ = s.runCurrentOnce(); return err }
	if err := s.validateBackup(candidate); err != nil { _ = os.RemoveAll(candidate); _ = s.runCurrentOnce(); return err }
	if err := os.WriteFile(filepath.Join(candidate, "metadata"), []byte("verified="+time.Now().Format(time.RFC3339)+"\n"), 0644); err != nil { return err }
	if err := os.WriteFile(filepath.Join(candidate, "HEALTHY"), nil, 0644); err != nil { return err }
	final := filepath.Join(backupsDir, stamp)
	if err := os.Rename(candidate, final); err != nil { return err }
	pruneBackups()
	return s.runCurrentOnce()
}

func pruneBackups() {
	list := healthyBackups()
	for _, dir := range list[maxBackups:] { _ = os.RemoveAll(dir) }
}

func pruneQuarantine() {
	ents, _ := os.ReadDir(quarantineDir)
	var dirs []string
	for _, e := range ents { if e.IsDir() { dirs = append(dirs, filepath.Join(quarantineDir, e.Name())) } }
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, dir := range dirs[maxQuarantine:] { _ = os.RemoveAll(dir) }
}

var _ = context.Background
var _ = strconv.Itoa
