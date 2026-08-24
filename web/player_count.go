package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	playerCountRecordSize = 10
	playerCountSampleRate = 5 * time.Second
	playerCountMaxPoints   = 2000
)

type PlayerCountPoint struct {
	Time  int64 `json:"time"`
	Count uint16 `json:"count"`
}

type PlayerCountResponse struct {
	Points []PlayerCountPoint `json:"points"`
	Total  int64              `json:"total"`
}

type PlayerCountStore struct {
	path string
	mu   sync.Mutex
}

func newPlayerCountStore(root string) *PlayerCountStore {
	return &PlayerCountStore{path: filepath.Join(root, "run", "player-count.bin")}
}

func (s *PlayerCountStore) append(point PlayerCountPoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return fmt.Errorf("create player count directory: %w", err)
	}

	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("open player count data: %w", err)
	}
	defer file.Close()

	size, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("seek player count data: %w", err)
	}
	if remainder := size % playerCountRecordSize; remainder != 0 {
		if err := file.Truncate(size - remainder); err != nil {
			return fmt.Errorf("repair incomplete player count record: %w", err)
		}
	}

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
	return file.Sync()
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
		return PlayerCountResponse{Points: []PlayerCountPoint{}, Total: total}, nil
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

func (s *PlayerCountStore) collect(root string) error {
	path := filepath.Join(root, "run", "server.health")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read server health log: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		count, ok := parsePlayerCount(lines[index])
		if !ok {
			continue
		}
		return s.append(PlayerCountPoint{Time: time.Now().Unix(), Count: count})
	}
	return nil
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

func startPlayerCountRecorder(root string) *PlayerCountStore {
	store := newPlayerCountStore(root)
	go func() {
		ticker := time.NewTicker(playerCountSampleRate)
		defer ticker.Stop()
		for {
			if err := store.collect(root); err != nil {
				fmt.Printf("player count recorder: %v\n", err)
			}
			<-ticker.C
		}
	}()
	return store
}

func writePlayerCountJSON(w interface{ Header() map[string][]string; Write([]byte) (int, error) }, value PlayerCountResponse) error {
	return json.NewEncoder(w).Encode(value)
}
