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
	logLines    = 20000
	webUser     = "censera"
)

type App struct {
	root        string
	password    string
	playerCount *PlayerCountStore
	mu          sync.Mutex
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

	password, err := loadOrCreatePassword(filepath.Join(root, "run", "web.credentials"))
	if err != nil {
		panic(err)
	}

	playerCount := newPlayerCountStore(root)
	app := &App{root: root, password: password, playerCount: playerCount}
	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handleIndex)
	mux.HandleFunc("/api/status", app.handleStatus)
	mux.HandleFunc("/api/logs", app.handleLogs)
	mux.HandleFunc("/api/events", app.handleEvents)
	mux.HandleFunc("/api/control", app.handleControl)
	mux.HandleFunc("/api/command", app.handleCommand)
	mux.HandleFunc("/api/player-count", playerCount.handle)
	startPlayerCountRecorder(playerCount)

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           securityHeaders(basicAuth(webUser, password, mux)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	fmt.Printf("Minecera web listening on http://%s\n", listenAddr)
	fmt.Printf("Minecera web credentials: %s\n", filepath.Join(root, "run", "web.credentials"))
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		panic(err)
	}
}

func loadOrCreatePassword(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		password := strings.TrimSpace(string(data))
		if password != "" {
			return password, nil
		}
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	password := hex.EncodeToString(buf)

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(password+"\n"), 0600); err != nil {
		return "", err
	}
	return password, nil
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

func basicAuth(user, password string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPassword, ok := r.BasicAuth()
		if !ok || gotUser != user || gotPassword != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="Minecera"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(autoHTML)
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	status, err := a.status()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

func (a *App) status() (Status, error) {
	return Status{State: "unknown", Updated: time.Now().Format(time.RFC3339)}, nil
}

func (a *App) handleLogs(w http.ResponseWriter, r *http.Request) {
	lines, err := a.logs()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(lines)
}

func (a *App) logs() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(a.root, "logs"))
	if err != nil {
		return nil, fmt.Errorf("read server logs: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "server-") && strings.HasSuffix(entry.Name(), ".log") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return []string{}, nil
	}
	data, err := os.ReadFile(filepath.Join(a.root, "logs", names[len(names)-1]))
	if err != nil {
		return nil, fmt.Errorf("read current server log: %w", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > logLines {
		lines = lines[len(lines)-logLines:]
	}
	return lines, nil
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	for {
		status, err := a.status()
		if err != nil {
			return
		}
		logs, err := a.logs()
		if err != nil {
			return
		}
		payload, err := json.Marshal(Snapshot{Status: status, Logs: logs})
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", payload)
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(statusEvery * time.Second):
		}
	}
}

func (a *App) handleControl(w http.ResponseWriter, r *http.Request) { http.Error(w, "not implemented", http.StatusNotImplemented) }
func (a *App) handleCommand(w http.ResponseWriter, r *http.Request) { http.Error(w, "not implemented", http.StatusNotImplemented) }

var _ = exec.Command
var _ = strconv.IntSize
var _ = syscall.Errno(0)
