package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const historySaveInterval = 30 * time.Second

// HistoryPoint stores a single point in time for historical charts.
type HistoryPoint struct {
	Timestamp     time.Time          `json:"timestamp"`
	CPUPercent    float64            `json:"cpu_percent"`
	MemPercent    float64            `json:"mem_percent"`
	DiskPercent   float64            `json:"disk_percent"`
	Disks         map[string]float64 `json:"disks,omitempty"`
	MemUsedBytes  uint64             `json:"mem_used_bytes"`
	DiskUsedBytes uint64             `json:"disk_used_bytes"`
}

// HistoryBuffer manages a thread-safe ring buffer of metric samples with optional disk persistence.
type HistoryBuffer struct {
	mu       sync.RWMutex
	saveMu   sync.Mutex
	capacity int
	points   []HistoryPoint
	filePath string
	lastSave time.Time
}

// NewHistoryBuffer creates a history buffer and loads persisted points if dataDir is set.
func NewHistoryBuffer(capacity int, dataDir string) *HistoryBuffer {
	if capacity <= 0 {
		capacity = 360
	}

	hb := &HistoryBuffer{
		capacity: capacity,
		points:   make([]HistoryPoint, 0, capacity),
		lastSave: time.Now(),
	}

	if dataDir != "" {
		if err := os.MkdirAll(dataDir, 0755); err != nil {
			log.Printf("Warning: unable to create data dir %s (%v); history will not persist", dataDir, err)
			return hb
		}
		hb.filePath = filepath.Join(dataDir, "history.json")
		hb.load()
	}

	return hb
}

// Add appends a new sample to the history buffer and periodically persists it.
func (h *HistoryBuffer) Add(p HistoryPoint) {
	var shouldSave bool

	h.mu.Lock()
	if len(h.points) >= h.capacity {
		h.points = append(h.points[1:], p)
	} else {
		h.points = append(h.points, p)
	}
	if h.filePath != "" && time.Since(h.lastSave) >= historySaveInterval {
		h.lastSave = time.Now()
		shouldSave = true
	}
	h.mu.Unlock()

	if shouldSave {
		go func() {
			if err := h.Save(); err != nil {
				log.Printf("Warning: failed saving history to %s: %v", h.filePath, err)
			}
		}()
	}
}

// GetAll returns a copy of all history points in chronological order.
func (h *HistoryBuffer) GetAll() []HistoryPoint {
	h.mu.RLock()
	defer h.mu.RUnlock()

	res := make([]HistoryPoint, len(h.points))
	copy(res, h.points)
	return res
}

// Save atomically writes the current history buffer to disk if persistence is enabled.
func (h *HistoryBuffer) Save() error {
	if h.filePath == "" {
		return nil
	}

	snapshot := h.GetAll()
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}

	h.saveMu.Lock()
	defer h.saveMu.Unlock()

	tmpPath := h.filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, h.filePath)
}

func (h *HistoryBuffer) load() {
	data, err := os.ReadFile(h.filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Warning: unable to read history file %s (%v); starting fresh", h.filePath, err)
		}
		return
	}

	var loaded []HistoryPoint
	if err := json.Unmarshal(data, &loaded); err != nil {
		log.Printf("Warning: invalid history JSON in %s (%v); starting fresh", h.filePath, err)
		return
	}

	if len(loaded) > h.capacity {
		loaded = loaded[len(loaded)-h.capacity:]
	}

	h.mu.Lock()
	h.points = make([]HistoryPoint, len(loaded), h.capacity)
	copy(h.points, loaded)
	h.mu.Unlock()

	log.Printf("Loaded %d historical points from %s", len(loaded), h.filePath)
}
