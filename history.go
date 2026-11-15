package main

import (
	"sync"
	"time"
)

// HistoryPoint stores a single point in time for historical charts.
type HistoryPoint struct {
	Timestamp     time.Time `json:"timestamp"`
	CPUPercent    float64   `json:"cpu_percent"`
	MemPercent    float64   `json:"mem_percent"`
	DiskPercent   float64   `json:"disk_percent"`
	MemUsedBytes  uint64    `json:"mem_used_bytes"`
	DiskUsedBytes uint64    `json:"disk_used_bytes"`
}

// HistoryBuffer manages a thread-safe ring buffer of metric samples.
type HistoryBuffer struct {
	mu       sync.RWMutex
	capacity int
	points   []HistoryPoint
}

// NewHistoryBuffer creates a history buffer with given capacity.
func NewHistoryBuffer(capacity int) *HistoryBuffer {
	if capacity <= 0 {
		capacity = 360
	}
	return &HistoryBuffer{
		capacity: capacity,
		points:   make([]HistoryPoint, 0, capacity),
	}
}

// Add appends a new sample to the history buffer.
func (h *HistoryBuffer) Add(p HistoryPoint) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.points) >= h.capacity {
		h.points = append(h.points[1:], p)
	} else {
		h.points = append(h.points, p)
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
