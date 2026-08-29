package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	healthTimeout = 15 * time.Second
	stopTimeout = 120 * time.Second
	worldRetries = 3
	maxBackups = 7
	maxQuarantine = 30
)

type Server struct {
	cmd *exec.Cmd
	stdin io.WriteCloser
	done chan error
	lines chan string
	log *os.File
	mu sync.Mutex
}

type Supervisor struct {
	server *Server
	control *os.File
	serverInput *os.File
	controlCh chan string
	serverInputCh chan string
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
	for _, dir := range []string{runDir, logsDir, backupsDir, quarantineDir} {
		if err := os.MkdirAll(dir, 0755); err != nil { panic(err) }
	}
	for _, path := range []string{controlFIFO, serverFIFO} {
		if err := ensureFIFO(path); err != nil { panic(err) }
	}

	control, err := os.OpenFile(controlFIFO, os.O_RDWR, 0660)
	if err != nil { panic(err) }
	defer control.Close()
	serverInput, err := os.OpenFile(serverFIFO, os.O_RDWR, 0660)
	if err != nil { panic(err) }
	defer serverInput.Close()

	s := &Supervisor{
		control: control,
		serverInput: serverInput,
		controlCh: make(chan string, 64),
		serverInputCh: make(chan string, 128),
		desired: true,
		recoveryAllowed: true,
	}
	go fifoReader(control, s.controlCh)
	go fifoReader(serverInput, s.serverInputCh)

	logf("Minecera supervisor started")
	for s.desired {
		err := s.monitorCurrent()
		if !s.desired { break }
		if err == nil { continue }
		if !s.recoveryAllowed { s.recoveryAllowed = true; continue }
		if s.retryCurrent() { continue }
		if !s.recoverFromBackup() {
			logf("automatic recovery failed; current world preserved")
			time.Sleep(10 * time.Second)
		}
	}
	logf("Minecera supervisor stopped")
}

func ensureFIFO(path string) error {
	if info, err := os.Stat(path); err == nil {
		if info.Mode()&os.ModeNamedPipe == 0 { return fmt.Errorf("%s exists and is not a FIFO", path) }
		return nil
	} else if !errors.Is(err, os.ErrNotExist) { return err }
	return syscall.Mkfifo(path, 0660)
}

func fifoReader(file *os.File, dst chan<- string) {
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" { continue }
		select { case dst <- line: default: logf("control queue full; dropped command: %s", line) }
	}
}

func (s *Supervisor) monitorCurrent() error {
	server, err := s.startProcess(root, true)
	if err != nil { return err }
	s.server = server
	if err := s.waitReady(server, true); err != nil {
		s.server = nil
		return err
	}
	logf("Minecraft healthy")

	backupTicker := time.NewTicker(time.Minute)
	defer backupTicker.Stop()

	for {
		select {
		case command := <-s.controlCh:
			if err := s.handleControl(command); err != nil { logf("command failed: %v", err) }
			if s.server == nil || !s.desired { return nil }
		case command := <-s.serverInputCh:
			if s.server != nil {
				if err := writeCommand(s.server, command); err != nil { logf("console command failed: %v", err) }
			}
		case <-backupTicker.C:
			if backupDue() {
				if err := s.createBackup(); err != nil { logf("daily backup failed: %v", err) }
			}
		case err := <-server.done:
			s.server = nil
			if err != nil { logf("Minecraft exited: %v", err) }
			else { logf("Minecraft stopped") }
			return err
		}
	}
}

func (s *Supervisor) startProcess(dir string, live bool) (*Server, error) {
	if _, err := os.Stat(dir); err != nil { return nil, err }
	if !live {
		if err := os.WriteFile(filepath.Join(dir, "eula.txt"), []byte("eula=true\n"), 0644); err != nil { return nil, err }
	}

	name := "server-" + time.Now().Format("20060102-150405.000") + ".log"
	if !live { name = "backup-test-" + time.Now().Format("20060102-150405.000") + ".log" }
	logFile, err := os.OpenFile(filepath.Join(logsDir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil { return nil, err }

	cmd := exec.Command("bash", startScript)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if !live {
		cmd.Env = append(cmd.Env,
			"MINECERA_ROOT="+dir,
			"MINECERA_JAR="+filepath.Join(dir, "paper-26.2.jar"),
			"MINECERA_XMS=512M",
			"MINECERA_XMX=1G",
		)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil { _ = logFile.Close(); return nil, err }
	stdout, err := cmd.StdoutPipe()
	if err != nil { _ = stdin.Close(); _ = logFile.Close(); return nil, err }
	stderr, err := cmd.StderrPipe()
	if err != nil { _ = stdin.Close(); _ = stdout.Close(); _ = logFile.Close(); return nil, err }
	if err := cmd.Start(); err != nil { _ = logFile.Close(); return nil, err }

	server := &Server{cmd: cmd, stdin: stdin, done: make(chan error, 1), lines: make(chan string, 1024), log: logFile}
	var wg sync.WaitGroup
	copyOutput := func(reader io.Reader) {
		defer wg.Done()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			server.mu.Lock()
			_, _ = fmt.Fprintln(server.log, line)
			server.mu.Unlock()
			select { case server.lines <- line: default: }
		}
	}
	wg.Add(2)
	go copyOutput(stdout)
	go copyOutput(stderr)
	go func() {
		err := cmd.Wait()
		wg.Wait()
		_ = logFile.Close()
		_ = stdin.Close()
		server.done <- err
	}()
	logf("starting Minecraft")
	return server, nil
}

func (s *Supervisor) waitReady(server *Server, allowControl bool) error {
	deadline := time.NewTimer(startupTimeout)
	defer deadline.Stop()
	for {
		select {
		case line := <-server.lines:
			if strings.Contains(line, "Done (") {
				if err := s.healthCheck(server, allowControl); err != nil { _ = s.killAndWait(server); return err }
				return nil
			}
		case err := <-server.done:
			return fmt.Errorf("startup failed: %w", err)
		case command := <-s.controlCh:
			if allowControl && command == "stop" { s.desired = false; _ = s.killAndWait(server); return fmt.Errorf("startup cancelled") }
		case <-deadline.C:
			_ = s.killAndWait(server)
			return fmt.Errorf("startup timeout")
		}
	}
}

func (s *Supervisor) healthCheck(server *Server, allowControl bool) error {
	if err := writeCommand(server, "list"); err != nil { return err }
	deadline := time.NewTimer(healthTimeout)
	defer deadline.Stop()
	for {
		select {
		case line := <-server.lines:
			if strings.Contains(line, "There are ") && strings.Contains(line, " players online:") { return nil }
		case err := <-server.done:
			return fmt.Errorf("health check failed: %w", err)
		case command := <-s.controlCh:
			if allowControl && command == "stop" { s.desired = false; _ = s.killAndWait(server); return fmt.Errorf("health check cancelled") }
		case <-deadline.C:
			return fmt.Errorf("health check timeout")
		}
	}
}

func (s *Supervisor) killAndWait(server *Server) error {
	if server == nil || server.cmd == nil || server.cmd.Process == nil { return nil }
	_ = server.cmd.Process.Kill()
	return <-server.done
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
		err := s.killAndWait(server)
		s.server = nil
		logf("graceful stop timed out; forced Minecraft down")
		return err
	}
}

func (s *Supervisor) handleControl(command string) error {
	switch command {
	case "status":
		if s.server == nil { fmt.Println("stopped") } else { fmt.Printf("running pid=%d\n", s.server.cmd.Process.Pid) }
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

func (s *Supervisor) retryCurrent() bool {
	for i := 1; i <= worldRetries; i++ {
		if !s.desired { return false }
		logf("retrying current world (%d/%d)", i, worldRetries)
		if err := s.monitorCurrent(); err == nil {
			if !s.desired { return false }
			continue
		}
	}
	return false
}

func healthyBackups() []string {
	entries, _ := os.ReadDir(backupsDir)
	var list []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") { continue }
		if _, err := os.Stat(filepath.Join(backupsDir, e.Name(), "HEALTHY")); err == nil { list = append(list, filepath.Join(backupsDir, e.Name())) }
	}
	sort.Sort(sort.Reverse(sort.StringSlice(list)))
	return list
}

func (s *Supervisor) recoverFromBackup() bool {
	logf("entering automatic recovery")
	for _, backup := range healthyBackups() {
		if err := s.validateBackup(backup); err != nil { logf("backup rejected: %s: %v", filepath.Base(backup), err); continue }
		name := "world-before-recovery-" + time.Now().Format("20060102-150405")
		preserved := filepath.Join(quarantineDir, name)
		if err := os.Rename(worldDir, preserved); err != nil { logf("cannot preserve world: %v", err); return false }
		if err := copyTree(filepath.Join(backup, "world"), worldDir); err != nil {
			_ = os.RemoveAll(worldDir); _ = os.Rename(preserved, worldDir); logf("restore failed: %v", err); continue
		}
		logf("restoring backup: %s (preserved %s)", filepath.Base(backup), name)
		if err := s.monitorCurrent(); err == nil {
			logf("automatic recovery successful: %s", filepath.Base(backup))
			return true
		}
		logf("restored world failed; restoring preserved world")
		if s.server != nil { _ = s.stopServer() }
		_ = os.RemoveAll(worldDir)
		if err := os.Rename(preserved, worldDir); err != nil { logf("could not restore preserved world: %v", err); return false }
	}
	return false
}

func (s *Supervisor) validateBackup(backup string) error {
	probe := filepath.Join(runDir, "probe")
	_ = os.RemoveAll(probe)
	if err := os.MkdirAll(probe, 0755); err != nil { return err }
	defer os.RemoveAll(probe)
	for _, name := range []string{"plugins", "config", "bukkit.yml", "commands.yml", "spigot.yml", "server.properties", "paper-26.2.jar"} {
		src := filepath.Join(root, name)
		if _, err := os.Stat(src); err != nil {
			if name == "config" { continue }
			return fmt.Errorf("missing %s", name)
		}
		if err := copyTree(src, filepath.Join(probe, name)); err != nil { return err }
	}
	if err := copyTree(filepath.Join(backup, "world"), filepath.Join(probe, "world")); err != nil { return err }
	if err := os.WriteFile(filepath.Join(probe, "eula.txt"), []byte("eula=true\n"), 0644); err != nil { return err }
	server, err := s.startProcess(probe, false)
	if err != nil { return err }
	if err := s.waitReady(server, false); err != nil { return err }
	_ = writeCommand(server, "stop")
	select { case err := <-server.done: return err; case <-time.After(stopTimeout): _ = server.cmd.Process.Kill(); return <-server.done }
}

func (s *Supervisor) createBackup() error {
	if s.server == nil { return fmt.Errorf("server is not running") }
	if err := writeCommand(s.server, "save-all flush"); err != nil { return err }
	time.Sleep(3 * time.Second)
	if err := s.stopServer(); err != nil { return err }
	stamp := time.Now().Format("20060102-040002")
	candidate := filepath.Join(backupsDir, ".candidate-"+time.Now().Format("20060102-150405"))
	_ = os.RemoveAll(candidate)
	if err := copyTree(worldDir, filepath.Join(candidate, "world")); err != nil { _ = s.restartAfterBackupFailure(); return err }
	if err := s.validateBackup(candidate); err != nil { _ = os.RemoveAll(candidate); _ = s.restartAfterBackupFailure(); return err }
	_ = os.WriteFile(filepath.Join(candidate, "metadata"), []byte("verified="+time.Now().Format(time.RFC3339)+"\ncreated="+stamp+"\n"), 0644)
	_ = os.WriteFile(filepath.Join(candidate, "HEALTHY"), nil, 0644)
	final := filepath.Join(backupsDir, time.Now().Format("20060102-150405"))
	if err := os.Rename(candidate, final); err != nil { _ = s.restartAfterBackupFailure(); return err }
	_ = os.WriteFile(filepath.Join(runDir, "last-backup-date"), []byte(time.Now().Format("2006-01-02")+"\n"), 0644)
	pruneBackups()
	return s.restartAfterBackupFailure()
}

func (s *Supervisor) restartAfterBackupFailure() error {
	if !s.desired { s.desired = true }
	return s.monitorCurrent()
}

func backupDue() bool {
	last, err := os.ReadFile(filepath.Join(runDir, "last-backup-date"))
	return time.Now().Hour() >= 4 && (err != nil || strings.TrimSpace(string(last)) != time.Now().Format("2006-01-02"))
}

func pruneBackups() {
	list := healthyBackups()
	for _, dir := range list[maxBackups:] { _ = os.RemoveAll(dir) }
}

func pruneQuarantine() {
	entries, _ := os.ReadDir(quarantineDir)
	var dirs []string
	for _, e := range entries { if e.IsDir() { dirs = append(dirs, filepath.Join(quarantineDir, e.Name())) } }
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, dir := range dirs[maxQuarantine:] { _ = os.RemoveAll(dir) }
}

func copyTree(src, dst string) error {
	info, err := os.Stat(src); if err != nil { return err }
	if info.IsDir() { if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil { return err } }
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil { return err }
		rel, err := filepath.Rel(src, path); if err != nil { return err }
		target := filepath.Join(dst, rel)
		if info.IsDir() { return os.MkdirAll(target, info.Mode().Perm()) }
		in, err := os.Open(path); if err != nil { return err }
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm()); if err != nil { return err }
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil { return copyErr }
		return closeErr
	})
}

func _() { _ = syscall.EINTR }
