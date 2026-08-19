package main

import (
	_ "embed"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

//go:embed index.html
var autoHTML []byte

const (
	defaultRoot = "/home/censera/minecraft"
	listenAddr  = "127.0.0.1:8080"
	statusEvery = 5
)

type App struct {
	root  string
	token string
	mu    sync.Mutex
}

type Status struct {
	State        string `json:"state"`
	PID          int    `json:"pid"`
	Uptime       string `json:"uptime"`
	CPU          string `json:"cpu"`
	Memory       string `json:"memory"`
	Load         string `json:"load"`
	Disk         string `json:"disk"`
	Backups      int    `json:"backups"`
	LastBackup   string `json:"lastBackup"`
	JournalEvent string `json:"journalEvent"`
	Updated      string `json:"updated"`
}

type Snapshot struct {
	Status Status   `json:"status"`
	Logs   []string `json:"logs"`
}

func main() {
	root := os.Getenv("MINECERA_ROOT")
	if root == "" {
		root = defaultRoot
	}

	token, err := randomToken()
	if err != nil {
		panic(err)
	}

	app := &App{root: root, token: token}
	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handleIndex)
	mux.HandleFunc("/api/status", app.handleStatus)
	mux.HandleFunc("/api/logs", app.handleLogs)
	mux.HandleFunc("/api/events", app.handleEvents)
	mux.HandleFunc("/api/control", app.handleControl)
	mux.HandleFunc("/api/command", app.handleCommand)

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	fmt.Printf("Minecera web listening on http://%s\n", listenAddr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		panic(err)
	}
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "minecera_session",
		Value:    a.token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(autoHTML)
}

func (a *App) authorized(r *http.Request) bool {
	cookie, err := r.Cookie("minecera_session")
	return err == nil && cookie.Value == a.token
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.snapshot(false))
}

func (a *App) handleLogs(w http.ResponseWriter, r *http.Request) {
	lines := 200
	if raw := r.URL.Query().Get("lines"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= 1000 {
			lines = n
		}
	}
	writeJSON(w, a.logs(lines))
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	status := a.status()
	logs := a.logs(160)

	if err := writeEvent(w, Snapshot{Status: status, Logs: logs}); err != nil {
		return
	}
	flusher.Flush()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	counter := 0
	for {
		select {
		case <-ticker.C:
			counter++
			logs = a.logs(160)
			if counter >= statusEvery {
				status = a.status()
				counter = 0
			}
			if err := writeEvent(w, Snapshot{Status: status, Logs: logs}); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (a *App) handleControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.authorized(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var body struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	allowed := map[string]bool{
		"start": true,
		"stop": true,
		"restart": true,
		"backup": true,
		"save": true,
		"reload": true,
	}
	if !allowed[body.Action] {
		http.Error(w, "invalid action", http.StatusBadRequest)
		return
	}

	if err := a.sendControl(body.Action); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]string{"status": "accepted", "action": body.Action})
}

func (a *App) handleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.authorized(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var request struct {
		Command string `json:"command"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	request.Command = strings.TrimSpace(request.Command)
	if request.Command == "" || strings.ContainsAny(request.Command, "\r\n") {
		http.Error(w, "invalid command", http.StatusBadRequest)
		return
	}

	if err := a.sendMinecraft(request.Command); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]string{"status": "accepted"})
}

func (a *App) sendControl(command string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return writeFIFO(filepath.Join(a.root, "run", "control"), command)
}

func (a *App) sendMinecraft(command string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return writeFIFO(filepath.Join(a.root, "run", "server.stdin"), command)
}

func writeFIFO(path, value string) error {
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open control channel: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = syscall.Close(fd)
		return fmt.Errorf("open control channel: invalid file")
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, value); err != nil {
		return fmt.Errorf("write control channel: %w", err)
	}
	return nil
}

func (a *App) snapshot(includeLogs bool) Snapshot {
	status := a.status()
	var logs []string
	if includeLogs {
		logs = a.logs(160)
	}
	return Snapshot{Status: status, Logs: logs}
}

func (a *App) status() Status {
	pid := findServerPID()
	status := Status{
		State:   "offline",
		PID:     pid,
		Load:    readLoad(),
		Disk:    diskUsage(a.root),
		Backups: backupCount(a.root),
		Updated: time.Now().Format(time.RFC3339),
	}

	status.LastBackup = latestBackup(a.root)
	status.JournalEvent = latestJournalEvent()

	if pid == 0 {
		return status
	}

	status.State = "running"
	status.Uptime, status.CPU, status.Memory = processStats(pid)
	return status
}

func findServerPID() int {
	out, err := exec.Command("pgrep", "-f", `^java .*paper-26\.2\.jar`).Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(line); err == nil {
			return pid
		}
	}
	return 0
}

func processStats(pid int) (string, string, string) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "etime=,%cpu=,rss=").Output()
	if err != nil {
		return "-", "-", "-"
	}
	fields := strings.Fields(string(out))
	if len(fields) < 3 {
		return "-", "-", "-"
	}
	rss, _ := strconv.ParseFloat(fields[2], 64)
	return fields[0], fields[1] + "%", fmt.Sprintf("%.1fG", rss/1024/1024)
}

func readLoad() string {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "-"
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return "-"
	}
	return strings.Join(fields[:3], " ")
}

func diskUsage(root string) string {
	out, err := exec.Command("df", "-h", root).Output()
	if err != nil {
		return "-"
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return "-"
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 5 {
		return "-"
	}
	return fmt.Sprintf("%s/%s (%s)", fields[2], fields[1], fields[4])
}

func backupCount(root string) int {
	matches, _ := filepath.Glob(filepath.Join(root, "backups", "*", "HEALTHY"))
	return len(matches)
}

func latestBackup(root string) string {
	entries, err := os.ReadDir(filepath.Join(root, "backups"))
	if err != nil {
		return "-"
	}

	best := ""
	for _, entry := range entries {
		if !entry.IsDir() || best >= entry.Name() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "backups", entry.Name(), "HEALTHY")); err == nil {
			best = entry.Name()
		}
	}
	if best == "" {
		return "-"
	}
	return best
}

func latestJournalEvent() string {
	out, err := exec.Command("journalctl", "-u", "minecera.service", "-n", "1", "-o", "cat", "--no-pager").Output()
	if err != nil {
		return "journal unavailable"
	}
	return strings.TrimSpace(string(out))
}

func (a *App) logs(lines int) []string {
	files, _ := filepath.Glob(filepath.Join(a.root, "logs", "server-*.log"))
	if len(files) == 0 {
		return []string{"no Minecraft server log exists"}
	}

	sort.Slice(files, func(i, j int) bool {
		aInfo, aErr := os.Stat(files[i])
		bInfo, bErr := os.Stat(files[j])
		if aErr != nil || bErr != nil {
			return files[i] > files[j]
		}
		return aInfo.ModTime().After(bInfo.ModTime())
	})

	data, err := os.ReadFile(files[0])
	if err != nil {
		return []string{"cannot read Minecraft log: " + err.Error()}
	}

	all := splitLines(string(data))
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return all
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r", "")
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func filterProbeLines(lines []string) []string {
	filtered := lines[:0]
	for _, line := range lines {
		if strings.Contains(line, "There are ") && strings.Contains(line, " of a max of ") && strings.Contains(line, " players online:") {
			continue
		}
		filtered = append(filtered, line)
	}
	return filtered
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeEvent(w http.ResponseWriter, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", data)
	return err
}
