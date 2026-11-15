package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web/*
var embeddedWebFS embed.FS

// Config holds runtime options parsed from command-line flags.
type Config struct {
	Port              int
	Host              string
	DockerSocket      string
	HostRoot          string
	DiskDirs          []string
	TopDirLimit       int
	PollInterval      time.Duration
	DiskScanInterval  time.Duration
	HistoryPoints     int
	BorgmaticCmd      string
	BorgmaticFile     string
	BorgmaticInterval time.Duration
	Demo              bool
}

// CombinedPayload aggregates all telemetry for the frontend.
type CombinedPayload struct {
	System      SystemMetrics   `json:"system"`
	History     []HistoryPoint  `json:"history"`
	Containers  []ContainerInfo `json:"containers"`
	Borgmatic   BorgmaticStats  `json:"borgmatic"`
	ServerTime  time.Time       `json:"server_time"`
	DockerError string          `json:"docker_error,omitempty"`
}

// Server holds collectors, historical buffer, and latest cached readings.
type Server struct {
	cfg       *Config
	sysColl   *SystemCollector
	dockColl  *DockerCollector
	borgColl  *BorgmaticCollector
	history   *HistoryBuffer
	mu        sync.RWMutex
	latestSys SystemMetrics
	latestDoc []ContainerInfo
	docErr    string
}

func envString(key, fallback string) string {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}
	return fallback
}

func envInt(key string, fallback int) int {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return fallback
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		log.Printf("Warning: invalid integer for %s=%q (%v), using default %d", key, val, err, fallback)
		return fallback
	}
	return n
}

func envDuration(key string, fallback time.Duration) time.Duration {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return fallback
	}
	d, err := time.ParseDuration(val)
	if err != nil {
		log.Printf("Warning: invalid duration for %s=%q (%v), using default %v", key, val, err, fallback)
		return fallback
	}
	return d
}

func envBool(key string, fallback bool) bool {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return fallback
	}
	b, err := strconv.ParseBool(val)
	if err != nil {
		log.Printf("Warning: invalid boolean for %s=%q (%v), using default %v", key, val, err, fallback)
		return fallback
	}
	return b
}

func loadConfigFromEnv() *Config {
	cfg := &Config{
		Port:              envInt("LIZARD_PORT", 8080),
		Host:              envString("LIZARD_HOST", "0.0.0.0"),
		DockerSocket:      envString("LIZARD_DOCKER_SOCKET", "/var/run/docker.sock"),
		HostRoot:          envString("LIZARD_HOST_ROOT", "/"),
		TopDirLimit:       envInt("LIZARD_TOP_DIR_LIMIT", 10),
		PollInterval:      envDuration("LIZARD_POLL_INTERVAL", 2*time.Second),
		DiskScanInterval:  envDuration("LIZARD_DISK_SCAN_INTERVAL", 2*time.Minute),
		HistoryPoints:     envInt("LIZARD_HISTORY_POINTS", 360),
		BorgmaticCmd:      envString("LIZARD_BORGMATIC_CMD", ""),
		BorgmaticFile:     envString("LIZARD_BORGMATIC_FILE", ""),
		BorgmaticInterval: envDuration("LIZARD_BORGMATIC_INTERVAL", 5*time.Minute),
		Demo:              envBool("LIZARD_DEMO", false),
	}

	diskDirsStr := envString("LIZARD_DISK_DIRS", "/")
	for _, d := range strings.Split(diskDirsStr, ",") {
		d = strings.TrimSpace(d)
		if d != "" {
			cfg.DiskDirs = append(cfg.DiskDirs, d)
		}
	}
	if len(cfg.DiskDirs) == 0 {
		cfg.DiskDirs = []string{"/"}
	}

	return cfg
}

func main() {
	cfg := loadConfigFromEnv()

	log.Printf("Starting Lizard Server Monitor on %s:%d ...", cfg.Host, cfg.Port)
	log.Printf("  Docker Socket:     %s", cfg.DockerSocket)
	log.Printf("  Host Root:         %s", cfg.HostRoot)
	log.Printf("  Disk Dirs:         %v", cfg.DiskDirs)
	if cfg.BorgmaticFile != "" {
		log.Printf("  Borgmatic File:    %s", cfg.BorgmaticFile)
	}
	if cfg.BorgmaticCmd != "" {
		log.Printf("  Borgmatic Command: %s", cfg.BorgmaticCmd)
	}
	if cfg.Demo {
		log.Printf("  Demo Mode:         ENABLED")
	}

	srv := &Server{
		cfg:      cfg,
		sysColl:  NewSystemCollector(cfg.HostRoot, cfg.DiskDirs, cfg.TopDirLimit, cfg.DiskScanInterval, cfg.Demo),
		dockColl: NewDockerCollector(cfg.DockerSocket, cfg.Demo),
		borgColl: NewBorgmaticCollector(cfg.BorgmaticCmd, cfg.BorgmaticFile, cfg.BorgmaticInterval, cfg.Demo),
		history:  NewHistoryBuffer(cfg.HistoryPoints),
	}

	// Initial poll
	srv.tick()

	// Background poll loop
	go func() {
		ticker := time.NewTicker(cfg.PollInterval)
		defer ticker.Stop()
		for range ticker.C {
			srv.tick()
		}
	}()

	// HTTP routes
	mux := http.NewServeMux()
	mux.HandleFunc("/api/all", srv.handleAll)
	mux.HandleFunc("/api/history", srv.handleHistory)
	mux.HandleFunc("/api/stats", srv.handleStats)
	mux.HandleFunc("/healthz", srv.handleHealth)

	webSubFS, err := fs.Sub(embeddedWebFS, "web")
	if err != nil {
		log.Fatalf("failed creating sub filesystem for web assets: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(webSubFS)))

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	httpServer := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server exited with error: %v", err)
		}
	}()

	log.Printf("Dashboard accessible at http://%s", addr)
	<-stop
	log.Println("Shutting down Lizard...")
}

func (s *Server) tick() {
	sys := s.sysColl.Collect()
	containers, docErr := s.dockColl.Collect()

	s.mu.Lock()
	s.latestSys = sys
	s.latestDoc = containers
	if docErr != nil {
		s.docErr = docErr.Error()
	} else {
		s.docErr = ""
	}
	s.mu.Unlock()

	s.history.Add(HistoryPoint{
		Timestamp:     sys.Timestamp,
		CPUPercent:    sys.CPUPercent,
		MemPercent:    sys.Memory.UsedPercent,
		DiskPercent:   sys.Disk.UsedPercent,
		MemUsedBytes:  sys.Memory.Used,
		DiskUsedBytes: sys.Disk.Used,
	})
}

func (s *Server) handleAll(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	sys := s.latestSys
	containers := s.latestDoc
	docErr := s.docErr
	s.mu.RUnlock()

	payload := CombinedPayload{
		System:      sys,
		History:     s.history.GetAll(),
		Containers:  containers,
		Borgmatic:   s.borgColl.Collect(),
		ServerTime:  time.Now(),
		DockerError: docErr,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.history.GetAll())
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	sys := s.latestSys
	containers := s.latestDoc
	docErr := s.docErr
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"system":       sys,
		"containers":   containers,
		"docker_error": docErr,
		"timestamp":    time.Now(),
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
