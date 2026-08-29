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
	lockPath = runDir + "/supervisor.lock"

	startupTimeout = 180 * time.Second
	healthTimeout = 15 * time.Second
	stopTimeout = 120 * time.Second
	currentRetries = 3
	maxBackups = 7
)

type Process struct {
	cmd *exec.Cmd
	stdin io.WriteCloser
	lines chan string
	done chan struct{}
	mu sync.Mutex
	err error
	log *os.File
}

type Supervisor struct {
	process *Process
	controlCh chan string
	serverInputCh chan string
	control *os.File
	serverInput *os.File
	desired bool
	noRecoveryOnce bool
}

func logf(format string, args ...any) {
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
	fmt.Print(line)
	if err := os.MkdirAll(logsDir, 0755); err != nil { return }
	f, err := os.OpenFile(filepath.Join(logsDir, "supervisor.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil { _, _ = f.WriteString(line); _ = f.Close() }
}

var lockFD int

func main() {
	for _, dir := range []string{runDir, logsDir, backupsDir, quarantineDir} {
		if err := os.MkdirAll(dir, 0755); err != nil { panic(err) }
	}
	if err := acquireLock(); err != nil { panic(err) }
	defer releaseLock()
	if err := ensureFIFO(controlFIFO); err != nil { panic(err) }
	if err := ensureFIFO(serverFIFO); err != nil { panic(err) }

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
	}
	go fifoReader(control, s.controlCh)
	go fifoReader(serverInput, s.serverInputCh)

	logf("Minecera supervisor started")
	defer func() {
		_ = os.Remove(controlFIFO)
		_ = os.Remove(serverFIFO)
		logf("Minecera supervisor stopped")
	}()

	for s.desired {
		err := s.runCurrent()
		if !s.desired { break }
		if err == nil { continue }
		if s.noRecoveryOnce {
			s.noRecoveryOnce = false
			continue
		}
		if s.retryCurrent() { continue }
		if s.recoverFromBackups() { continue }
		logf("automatic recovery exhausted; current world preserved")
		time.Sleep(10 * time.Second)
	}
}

func acquireLock() error {
	fd, err := syscall.Open(lockPath, syscall.O_CREATE|syscall.O_RDWR, 0660)
	if err != nil { return err }
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = syscall.Close(fd)
		return fmt.Errorf("another Minecera supervisor is already running")
	}
	lockFD = fd
	return nil
}

func releaseLock() {
	if lockFD == 0 { return }
	_ = syscall.Flock(lockFD, syscall.LOCK_UN)
	_ = syscall.Close(lockFD)
	lockFD = 0
}

func ensureFIFO(path string) error {
	info, err := os.Stat(path)
	if err == nil {
		if info.Mode()&os.ModeNamedPipe == 0 { return fmt.Errorf("%s exists and is not a FIFO", path) }
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) { return err }
	return syscall.Mkfifo(path, 0660)
}

func fifoReader(file *os.File, dst chan<- string) {
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" { continue }
		select {
		case dst <- line:
		default: logf("command queue full; dropped command: %s", line)
		}
	}
}

func (s *Supervisor) runCurrent() error {
	p, err := s.startChecked(root, true)
	if err != nil { return err }
	s.process = p
	defer func() { if s.process == p { s.process = nil } }()
	logf("Minecraft healthy")
	return s.monitor(p)
}

func (s *Supervisor) startChecked(dir string, live bool) (*Process, error) {
	if _, err := os.Stat(dir); err != nil { return nil, err }
	if !live {
		if err := os.WriteFile(filepath.Join(dir, "eula.txt"), []byte("eula=true\n"), 0644); err != nil { return nil, err }
	}

	stamp := time.Now().Format("20060102-150405.000000000")
	name := "server-" + stamp + ".log"
	if !live { name = "backup-test-" + stamp + ".log" }
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
	if err := cmd.Start(); err != nil { _ = stdin.Close(); _ = stdout.Close(); _ = stderr.Close(); _ = logFile.Close(); return nil, err }

	p := &Process{cmd: cmd, stdin: stdin, lines: make(chan string, 8192), done: make(chan struct{}), log: logFile}
	var wg sync.WaitGroup
	copyOutput := func(r io.Reader) {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			p.mu.Lock()
			_, _ = fmt.Fprintln(p.log, line)
			p.mu.Unlock()
			select { case p.lines <- line: default: }
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
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		close(p.done)
	}()

	logf("starting Minecraft")
	if err := s.waitReady(p); err != nil {
		_ = p.kill()
		return nil, err
	}
	return p, nil
}

func (s *Supervisor) waitReady(p *Process) error {
	deadline := time.NewTimer(startupTimeout)
	defer deadline.Stop()
	for {
		select {
		case line := <-p.lines:
			if strings.Contains(line, "Done (") { return s.healthCheck(p) }
		case <-p.done:
			return fmt.Errorf("startup failed: %v", p.exitError())
		case command := <-s.controlCh:
			s.handleStartupCommand(p, command)
			if !s.desired { return fmt.Errorf("startup cancelled") }
		case <-deadline.C:
			if p.exited() { return fmt.Errorf("startup timeout: process exited") }
			_ = p.kill()
			return fmt.Errorf("startup timeout")
		}
	}
}

func (s *Supervisor) healthCheck(p *Process) error {
	if err := writeCommand(p, "list"); err != nil { return err }
	deadline := time.NewTimer(healthTimeout)
	defer deadline.Stop()
	for {
		select {
		case line := <-p.lines:
			if strings.Contains(line, "There are ") && strings.Contains(line, " players online:") { return nil }
		case <-p.done:
			return fmt.Errorf("health check failed: %v", p.exitError())
		case command := <-s.controlCh:
			s.handleStartupCommand(p, command)
			if !s.desired { return fmt.Errorf("health check cancelled") }
		case <-deadline.C:
			_ = p.kill()
			return fmt.Errorf("health check timeout")
		}
	}
}

func (s *Supervisor) handleStartupCommand(p *Process, command string) {
	switch command {
	case "stop":
		s.desired = false
		_ = p.kill()
	case "restart":
		s.noRecoveryOnce = true
		_ = p.kill()
	default:
		logf("ignoring command until server is ready: %s", command)
	}
}

func writeCommand(p *Process, command string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
		return fmt.Errorf("server is not running")
	default:
	}
	if p.stdin == nil { return fmt.Errorf("server is not running") }
	_, err := fmt.Fprintln(p.stdin, command)
	return err
}

func (p *Process) exited() bool {
	select { case <-p.done: return true; default: return false }
}

func (p *Process) exitError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *Process) kill() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil || p.exited() { return nil }
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) { return err }
	<-p.done
	return p.exitError()
}

func (s *Supervisor) stopProcess() error {
	p := s.process
	if p == nil { return nil }
	logf("stopping Minecraft")
	_ = writeCommand(p, "stop")
	select {
	case <-p.done:
		s.process = nil
		logf("Minecraft stopped")
		return p.exitError()
	case <-time.After(stopTimeout):
		_ = p.kill()
		s.process = nil
		logf("graceful stop timed out; forced Minecraft down")
		return p.exitError()
	}
}

func (s *Supervisor) handleControl(command string) error {
	switch command {
	case "status":
		if s.process == nil { fmt.Println("stopped") } else { fmt.Printf("running pid=%d\n", s.process.cmd.Process.Pid) }
	case "start":
		s.desired = true
	case "stop":
		s.desired = false
		return s.stopProcess()
	case "restart":
		s.desired = true
		s.noRecoveryOnce = true
		return s.stopProcess()
	case "backup":
		if s.process == nil { return fmt.Errorf("server is not running") }
		return s.createBackup(s.process)
	case "save":
		if s.process == nil { return fmt.Errorf("server is not running") }
		return writeCommand(s.process, "save-all flush")
	case "reload":
		if s.process == nil { return fmt.Errorf("server is not running") }
		return writeCommand(s.process, "reload confirm")
	default:
		if s.process == nil { return fmt.Errorf("server is not running") }
		return writeCommand(s.process, command)
	}
	return nil
}

func (s *Supervisor) retryCurrent() bool {
	for i := 1; i <= currentRetries; i++ {
		if !s.desired { return false }
		logf("retrying current world (%d/%d)", i, currentRetries)
		p, err := s.startChecked(root, true)
		if err != nil { logf("current world start failed: %v", err); continue }
		s.process = p
		err = s.monitor(p)
		s.process = nil
		if !s.desired { return false }
		if err == nil { return true }
	}
	return false
}

func (s *Supervisor) monitor(p *Process) error {
	backupTicker := time.NewTicker(time.Minute)
	defer backupTicker.Stop()
	for {
		select {
		case command := <-s.controlCh:
			if err := s.handleControl(command); err != nil { logf("command failed: %v", err) }
			if !s.desired || s.process == nil { return nil }
		case command := <-s.serverInputCh:
			if err := writeCommand(p, command); err != nil { logf("console command failed: %v", err) }
		case <-backupTicker.C:
			if backupDue() { return s.createBackup(p) }
		case <-p.done:
			return p.exitError()
		}
	}
}

func (s *Supervisor) createBackup(p *Process) error {
	if p == nil || p.exited() { return fmt.Errorf("server is not running") }
	if err := writeCommand(p, "save-all flush"); err != nil { return err }
	time.Sleep(3 * time.Second)
	if err := s.stopProcess(); err != nil { return err }

	stamp := time.Now().Format("20060102-150405")
	candidate := filepath.Join(backupsDir, ".candidate-"+stamp)
	final := filepath.Join(backupsDir, stamp)
	_ = os.RemoveAll(candidate)
	if err := copyTree(worldDir, filepath.Join(candidate, "world")); err != nil { _ = os.RemoveAll(candidate); return err }
	if err := s.validateBackup(candidate); err != nil { _ = os.RemoveAll(candidate); return err }
	if err := os.WriteFile(filepath.Join(candidate, "metadata"), []byte("verified="+time.Now().Format(time.RFC3339)+"\n"), 0644); err != nil { _ = os.RemoveAll(candidate); return err }
	if err := os.WriteFile(filepath.Join(candidate, "HEALTHY"), nil, 0644); err != nil { _ = os.RemoveAll(candidate); return err }
	if err := os.Rename(candidate, final); err != nil { _ = os.RemoveAll(candidate); return err }
	_ = os.WriteFile(filepath.Join(runDir, "last-backup-date"), []byte(time.Now().Format("2006-01-02")+"\n"), 0644)
	pruneBackups()
	logf("backup promoted healthy: %s", stamp)
	return nil
}

func backupDue() bool {
	last, err := os.ReadFile(filepath.Join(runDir, "last-backup-date"))
	return time.Now().Hour() >= 4 && (err != nil || strings.TrimSpace(string(last)) != time.Now().Format("2006-01-02"))
}

func healthyBackups() []string {
	entries, _ := os.ReadDir(backupsDir)
	list := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") { continue }
		if _, err := os.Stat(filepath.Join(backupsDir, e.Name(), "HEALTHY")); err == nil { list = append(list, filepath.Join(backupsDir, e.Name())) }
	}
	sort.Sort(sort.Reverse(sort.StringSlice(list)))
	return list
}

func pruneBackups() {
	list := healthyBackups()
	if len(list) <= maxBackups { return }
	for _, dir := range list[maxBackups:] {
		logf("removing old backup: %s", filepath.Base(dir))
		_ = os.RemoveAll(dir)
	}
}

func (s *Supervisor) validateBackup(backup string) error {
	if _, err := os.Stat(filepath.Join(backup, "world", "level.dat")); err != nil { return fmt.Errorf("invalid backup: missing world/level.dat") }
	probe := filepath.Join(runDir, "probe")
	_ = os.RemoveAll(probe)
	if err := os.MkdirAll(probe, 0755); err != nil { return err }
	defer os.RemoveAll(probe)

	for _, name := range []string{"plugins", "config", "bukkit.yml", "commands.yml", "spigot.yml", "server.properties", "paper-26.2.jar"} {
		src := filepath.Join(root, name)
		if _, err := os.Stat(src); err != nil {
			if name == "config" { continue }
			return fmt.Errorf("probe setup: missing %s", name)
		}
		if err := copyTree(src, filepath.Join(probe, name)); err != nil { return err }
	}
	if err := copyTree(filepath.Join(backup, "world"), filepath.Join(probe, "world")); err != nil { return err }
	if err := os.WriteFile(filepath.Join(probe, "eula.txt"), []byte("eula=true\n"), 0644); err != nil { return err }

	p, err := s.startChecked(probe, false)
	if err != nil { return err }
	if err := writeCommand(p, "stop"); err != nil { _ = p.kill(); return err }
	select {
	case <-p.done:
		if err := p.exitError(); err != nil { return fmt.Errorf("probe stop failed: %v", err) }
		return nil
	case <-time.After(stopTimeout):
		_ = p.kill()
		return fmt.Errorf("probe stop timeout")
	}
}

func (s *Supervisor) recoverFromBackups() bool {
	logf("entering automatic recovery")
	for _, backup := range healthyBackups() {
		if err := s.validateBackup(backup); err != nil {
			logf("backup rejected: %s: %v", filepath.Base(backup), err)
			continue
		}

		preserved := filepath.Join(quarantineDir, "world-before-recovery-"+time.Now().Format("20060102-150405.000000000"))
		if err := os.Rename(worldDir, preserved); err != nil {
			logf("FATAL: cannot preserve current world: %v", err)
			return false
		}
		if err := copyTree(filepath.Join(backup, "world"), worldDir); err != nil {
			_ = os.RemoveAll(worldDir)
			_ = os.Rename(preserved, worldDir)
			logf("restore failed: %v", err)
			continue
		}

		logf("restoring backup: %s", filepath.Base(backup))
		p, err := s.startChecked(root, true)
		if err == nil {
			s.process = p
			logf("automatic recovery successful: %s; preserved world: %s", filepath.Base(backup), filepath.Base(preserved))
			err = s.monitor(p)
			s.process = nil
			if !s.desired { return true }
			if err == nil { return true }
			return false
		}

		logf("restored world failed health verification; reverting")
		_ = os.RemoveAll(worldDir)
		if restoreErr := os.Rename(preserved, worldDir); restoreErr != nil {
			logf("FATAL: could not restore preserved world: %v", restoreErr)
			return false
		}
	}
	return false
}

func copyTree(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil { return err }
	if info.Mode()&os.ModeSymlink != 0 { return fmt.Errorf("symlink not allowed: %s", src) }
	if !info.IsDir() { return copyFile(src, dst, info.Mode().Perm()) }
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil { return err }
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil { return err }
		if path == src { return nil }
		rel, err := filepath.Rel(src, path)
		if err != nil { return err }
		target := filepath.Join(dst, rel)
		if info.Mode()&os.ModeSymlink != 0 { return fmt.Errorf("symlink not allowed: %s", path) }
		if info.IsDir() { return os.MkdirAll(target, info.Mode().Perm()) }
		if !info.Mode().IsRegular() { return fmt.Errorf("unsupported file type: %s", path) }
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil { return err }
	in, err := os.Open(src)
	if err != nil { return err }
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil { return err }
	if _, err := io.Copy(out, in); err != nil { _ = out.Close(); return err }
	return out.Close()
}
