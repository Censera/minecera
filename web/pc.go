package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const (
	playerCountRecordSize = 10
	playerCountSampleRate = 5 * time.Second
	playerCountMaxPoints  = 1000000
	minecraftStatusPort   = 25565
)

type PlayerCountPoint struct {
	Time int64 `json:"time"`
	Count uint16 `json:"count"`
}

type PlayerCountResponse struct {
	Points []PlayerCountPoint `json:"points"`
	Total int64 `json:"total"`
}

type PlayerCountStore struct {
	path string
	mu sync.Mutex
}

func newPlayerCountStore(root string) *PlayerCountStore {
	return &PlayerCountStore{path: filepath.Join(root, "run", "player-count.bin")}
}

func (s *PlayerCountStore) append(point PlayerCountPoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil { return fmt.Errorf("open player count data: %w", err) }
	defer file.Close()
	var record [playerCountRecordSize]byte
	binary.LittleEndian.PutUint64(record[0:8], uint64(point.Time))
	binary.LittleEndian.PutUint16(record[8:10], point.Count)
	if written, err := file.Write(record[:]); err != nil { return fmt.Errorf("append player count record: %w", err) } else if written != len(record) { return fmt.Errorf("append player count record: short write (%d/%d)", written, len(record)) }
	return file.Sync()
}

func (s *PlayerCountStore) read(maxPoints int) (PlayerCountResponse, error) {
	if maxPoints < 1 || maxPoints > playerCountMaxPoints { maxPoints = playerCountMaxPoints }
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) { return PlayerCountResponse{Points: []PlayerCountPoint{}}, nil }
		return PlayerCountResponse{}, fmt.Errorf("open player count data: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil { return PlayerCountResponse{}, fmt.Errorf("stat player count data: %w", err) }
	size := info.Size() - info.Size()%playerCountRecordSize
	total := size / playerCountRecordSize
	if total == 0 { return PlayerCountResponse{Points: []PlayerCountPoint{}, Total: 0}, nil }
	start := int64(0)
	count := total
	if count > int64(maxPoints) { start = count - int64(maxPoints); count = int64(maxPoints) }
	points := make([]PlayerCountPoint, 0, count)
	var record [playerCountRecordSize]byte
	for index := start; index < start+count; index++ {
		if _, err := file.ReadAt(record[:], index*playerCountRecordSize); err != nil { return PlayerCountResponse{}, fmt.Errorf("read player count record %d: %w", index, err) }
		points = append(points, PlayerCountPoint{Time: int64(binary.LittleEndian.Uint64(record[0:8])), Count: binary.LittleEndian.Uint16(record[8:10])})
	}
	return PlayerCountResponse{Points: points, Total: total}, nil
}

func (s *PlayerCountStore) sample() (bool, error) {
	count, err := minecraftPlayerCount()
	if err != nil { return false, nil }
	return true, s.append(PlayerCountPoint{Time: time.Now().Unix(), Count: count})
}

func minecraftPlayerCount() (uint16, error) {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", minecraftStatusPort), 2*time.Second)
	if err != nil { return 0, err }
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	var handshake []byte
	handshake = appendVarInt(handshake, 0)
	handshake = appendVarInt(handshake, 0)
	handshake = appendVarInt(handshake, len("localhost"))
	handshake = append(handshake, "localhost"...)
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], minecraftStatusPort)
	handshake = append(handshake, port[:]...)
	handshake = appendVarInt(handshake, 1)
	if err := writePacket(conn, handshake); err != nil { return 0, err }
	if err := writePacket(conn, []byte{0}); err != nil { return 0, err }
	if _, err := readVarInt(conn); err != nil { return 0, err }
	packetID, err := readVarInt(conn)
	if err != nil || packetID != 0 { return 0, fmt.Errorf("invalid status response packet") }
	length, err := readVarInt(conn)
	if err != nil || length < 0 || length > 1024*1024 { return 0, fmt.Errorf("invalid status response length") }
	payload := make([]byte, length)
	if _, err := io.ReadFull(conn, payload); err != nil { return 0, err }
	var status struct { Players struct { Online int `json:"online"` } `json:"players"` }
	if err := json.Unmarshal(payload, &status); err != nil { return 0, err }
	if status.Players.Online < 0 || status.Players.Online > 65535 { return 0, fmt.Errorf("invalid player count") }
	return uint16(status.Players.Online), nil
}

func writePacket(w io.Writer, payload []byte) error {
	length := appendVarInt(nil, len(payload))
	_, err := w.Write(append(length, payload...))
	return err
}

func appendVarInt(dst []byte, value int) []byte {
	for value&^0x7f != 0 { dst = append(dst, byte(value&0x7f|0x80)); value >>= 7 }
	return append(dst, byte(value))
}

func readVarInt(r io.Reader) (int, error) {
	value, shift := 0, 0
	for { var b [1]byte; if _, err := io.ReadFull(r, b[:]); err != nil { return 0, err }; value |= int(b[0]&0x7f) << shift; if b[0]&0x80 == 0 { return value, nil }; shift += 7; if shift > 28 { return 0, fmt.Errorf("invalid varint") } }
}

func startPlayerCountRecorder(store *PlayerCountStore) {
	go func() { ticker := time.NewTicker(playerCountSampleRate); defer ticker.Stop(); for range ticker.C { if _, err := store.sample(); err != nil { fmt.Printf("player count recorder: %v\n", err) } } }()
}

func (s *PlayerCountStore) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { http.Error(w, "method not allowed", http.StatusMethodNotAllowed); return }
	maxPoints := playerCountMaxPoints
	if raw := r.URL.Query().Get("points"); raw != "" { if value, err := strconv.Atoi(raw); err == nil && value > 0 && value <= playerCountMaxPoints { maxPoints = value } }
	response, err := s.read(maxPoints)
	if err != nil { http.Error(w, err.Error(), http.StatusInternalServerError); return }
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}
