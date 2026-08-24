package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	playerCountRecordSize = 10
	playerCountSampleRate = 5 * time.Second
	playerCountMaxPoints  = 2000
)

type PlayerCountPoint struct {
	Time  int64  `json:"time"`
	Count uint16 `json:"count"`
}

type PlayerCountResponse struct {
	Points []PlayerCountPoint `json:"points"`
	Total  int64              `json:"total"`
}

type PlayerCountStore struct {
	path       string
	healthPath string
	stdinPath  string
	mu         sync.Mutex
}

func newPlayerCountStore(root string) *PlayerCountStore {
	run := filepath.Join(root, "run")
	return &PlayerCountStore{
		path:       filepath.Join(run, "player-count.bin"),
		healthPath: filepath.Join(run, "server.health"),
		stdinPath:  filepath.Join(run, "server.stdin"),
	}
}

func (s *PlayerCountStore) append(point PlayerCountPoint) error {
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("open player count data: %w", err)
	}
	defer file.Close()

	var record [playerCountRecordSize]byte
	binary.LittleEndian.PutUint64(record[0:8], uint64(point.Time))
	binary.LittleEndian.PutUint16(record[8:10], point.Count)
	written, err := file.Write(record[:])
	if err != nil {
		return fmt.Errorf("append player count record: %w", err)
	}
	if written != len(record) {
		return fmt.Errorf("append player count record: short write (%d/%d)", written, len(record))
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync player count data: %w", err)
	}
	return nil
}

func (s *PlayerCountStore) read(maxPoints int) (PlayerCountResponse, error) {
	if maxPoints < 1 || maxPoints > playerCountMaxPoints {
		maxPoints = playerCountMaxPoints
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return PlayerCountResponse{Points: []PlayerCountPoint{}}, nil
		}
		return PlayerCountResponse{}, fmt.Errorf("open player count data: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return PlayerCountResponse{}, fmt.Errorf("stat player count data: %w", err)
	}

	size := info.Size() - info.Size()%playerCountRecordSize
	total := size / playerCountRecordSize
	if size == 0 {
		return PlayerCountResponse{Points: []PlayerCountPoint{}, Total: 0}, nil
	}

	stride := int64(1)
	if total > int64(maxPoints) {
		stride = (total + int64(maxPoints) - 1) / int64(maxPoints)
	}

	points := make([]PlayerCountPoint, 0, maxPoints)
	var record [playerCountRecordSize]byte
	for index := int64(0); index < total; index += stride {
		if _, err := file.ReadAt(record[:], index*playerCountRecordSize); err != nil {
			return PlayerCountResponse{}, fmt.Errorf("read player count record %d: %w", index, err)
		}
		points = append(points, PlayerCountPoint{
			Time:  int64(binary.LittleEndian.Uint64(record[0:8])),
			Count: binary.LittleEndian.Uint16(record[8:10]),
		})
	}

	return PlayerCountResponse{Points: points, Total: total}, nil
}

func (s *PlayerCountStore) sample() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(s.stdinPath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("check Minecraft input channel: %w", err)
	}
	info, err := os.Stat(s.healthPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat player count health log: %w", err)
	}
	offset := info.Size()

	stdin, err := os.OpenFile(s.stdinPath, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ENXIO) || errors.Is(err, syscall.ENOENT) {
			return false, nil
		}
		return false, fmt.Errorf("open Minecraft input channel: %w", err)
	}
	_, writeErr := io.WriteString(stdin, "list\n")
	closeErr := stdin.Close()
	if writeErr != nil {
		return false, fmt.Errorf("request player count: %w", writeErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close Minecraft input channel: %w", closeErr)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(s.healthPath)
		if err != nil {
			return false, fmt.Errorf("read player count health log: %w", err)
		}
		if int64(len(data)) <= offset {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		for _, line := range strings.Split(string(data[offset:]), "\n") {
			count, ok := parsePlayerCount(line)
			if !ok {
				continue
			}
			if err := s.append(PlayerCountPoint{Time: time.Now().Unix(), Count: count}); err != nil {
				return false, err
			}
			return true, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false, nil
}

func parsePlayerCount(line string) (uint16, bool) {
	const prefix = "There are "
	const middle = " of a max of "
	start := strings.Index(line, prefix)
	if start < 0 {
		return 0, false
	}
	start += len(prefix)
	end := strings.Index(line[start:], middle)
	if end < 0 {
		return 0, false
	}
	value := line[start : start+end]
	count, err := strconv.ParseUint(value, 10, 16)
	if err != nil {
		return 0, false
	}
	return uint16(count), true
}

func startPlayerCountRecorder(store *PlayerCountStore) {
	go func() {
		ticker := time.NewTicker(playerCountSampleRate)
		defer ticker.Stop()
		for range ticker.C {
			if _, err := store.sample(); err != nil {
				fmt.Printf("player count recorder: %v\n", err)
			}
		}
	}()
}

func (s *PlayerCountStore) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	maxPoints := playerCountMaxPoints
	if raw := r.URL.Query().Get("points"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 && value <= playerCountMaxPoints {
			maxPoints = value
		}
	}

	response, err := s.read(maxPoints)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		return
	}
}
