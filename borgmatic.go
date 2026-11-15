package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// BorgArchive represents one archive snapshot in a Borg repository.
type BorgArchive struct {
	Name              string    `json:"name"`
	Archive           string    `json:"archive"`
	Start             time.Time `json:"start"`
	StartTime         string    `json:"start_time"`
	Duration          string    `json:"duration"`
	OriginalSize      uint64    `json:"original_size"`
	CompressedSize    uint64    `json:"compressed_size"`
	DeduplicatedSize  uint64    `json:"deduplicated_size"`
	HumanOriginal     string    `json:"human_original"`
	HumanCompressed   string    `json:"human_compressed"`
	HumanDeduplicated string    `json:"human_deduplicated"`
	NFiles            int64     `json:"nfiles"`
	RepoLocation      string    `json:"repo_location"`
}

// BorgRepoInfo groups archives by repository.
type BorgRepoInfo struct {
	Location          string        `json:"location"`
	ID                string        `json:"id"`
	Archives          []BorgArchive `json:"archives"`
	TotalArchives     int           `json:"total_archives"`
	TotalOriginal     uint64        `json:"total_original"`
	TotalDeduplicated uint64        `json:"total_deduplicated"`
	HumanOriginal     string        `json:"human_original"`
	HumanDeduplicated string        `json:"human_deduplicated"`
}

// BorgmaticStats encapsulates repository lists and archive summaries.
type BorgmaticStats struct {
	Repositories []BorgRepoInfo `json:"repositories"`
	AllArchives  []BorgArchive  `json:"all_archives"`
	TotalBackups int            `json:"total_backups"`
	LastUpdated  time.Time      `json:"last_updated"`
	Error        string         `json:"error,omitempty"`
	ConfigStatus string         `json:"config_status,omitempty"`
}

// BorgmaticCollector periodically fetches and parses Borgmatic info.
type BorgmaticCollector struct {
	cmdPath     string
	filePath    string
	interval    time.Duration
	demoMode    bool
	mu          sync.RWMutex
	cachedStats BorgmaticStats
	lastRun     time.Time
}

// NewBorgmaticCollector creates a new Borgmatic collector.
func NewBorgmaticCollector(cmdPath, filePath string, interval time.Duration, demo bool) *BorgmaticCollector {
	return &BorgmaticCollector{
		cmdPath:  cmdPath,
		filePath: filePath,
		interval: interval,
		demoMode: demo,
	}
}

type rawRepoJSON struct {
	Repository struct {
		ID       string `json:"id"`
		Location string `json:"location"`
	} `json:"repository"`
	Archives []struct {
		Name    string `json:"name"`
		Archive string `json:"archive"`
		Start   string `json:"start"`
		End     string `json:"end"`
		Stats   struct {
			OriginalSize     uint64 `json:"original_size"`
			CompressedSize   uint64 `json:"compressed_size"`
			DeduplicatedSize uint64 `json:"deduplicated_size"`
			NFiles           int64  `json:"nfiles"`
		} `json:"stats"`
	} `json:"archives"`
}

// Collect returns cached Borgmatic stats, refreshing asynchronously if interval has elapsed.
func (b *BorgmaticCollector) Collect() BorgmaticStats {
	b.mu.Lock()
	now := time.Now()
	needRefresh := b.cachedStats.LastUpdated.IsZero() || now.Sub(b.lastRun) >= b.interval
	if needRefresh {
		b.lastRun = now
		b.mu.Unlock()

		if b.cachedStats.LastUpdated.IsZero() {
			stats := b.fetch()
			b.mu.Lock()
			b.cachedStats = stats
			b.mu.Unlock()
			return stats
		}

		go func() {
			stats := b.fetch()
			b.mu.Lock()
			b.cachedStats = stats
			b.mu.Unlock()
		}()
	} else {
		b.mu.Unlock()
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.cachedStats
}

// fetch reads from file, command, or returns demo data.
func (b *BorgmaticCollector) fetch() BorgmaticStats {
	if b.demoMode {
		return b.getDemoStats()
	}

	var rawBytes []byte

	if b.filePath != "" {
		if fi, err := os.Stat(b.filePath); err != nil {
			if os.IsNotExist(err) {
				return BorgmaticStats{
					LastUpdated:  time.Now(),
					ConfigStatus: "File Not Found",
					Error:        fmt.Sprintf("borgmatic file '%s' does not exist. Verify volume mount in docker-compose.yml", b.filePath),
				}
			}
			if os.IsPermission(err) {
				return BorgmaticStats{
					LastUpdated:  time.Now(),
					ConfigStatus: "Permission Denied",
					Error:        fmt.Sprintf("permission denied reading '%s'", b.filePath),
				}
			}
			return BorgmaticStats{
				LastUpdated:  time.Now(),
				ConfigStatus: "File Error",
				Error:        fmt.Sprintf("cannot access '%s': %v", b.filePath, err),
			}
		} else if fi.IsDir() {
			return BorgmaticStats{
				LastUpdated:  time.Now(),
				ConfigStatus: "Invalid Path",
				Error:        fmt.Sprintf("path '%s' is a directory, expected JSON file", b.filePath),
			}
		}

		data, err := os.ReadFile(b.filePath)
		if err != nil {
			return BorgmaticStats{
				LastUpdated:  time.Now(),
				ConfigStatus: "Read Error",
				Error:        fmt.Sprintf("failed reading '%s': %v", b.filePath, err),
			}
		}
		rawBytes = data
	} else if b.cmdPath != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "sh", "-c", b.cmdPath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(output))
			if msg == "" {
				msg = err.Error()
			}
			return BorgmaticStats{
				LastUpdated:  time.Now(),
				ConfigStatus: "Command Failed",
				Error:        fmt.Sprintf("command '%s' failed: %s", b.cmdPath, msg),
			}
		}
		rawBytes = output
	} else {
		return BorgmaticStats{
			LastUpdated:  time.Now(),
			ConfigStatus: "Not Configured",
			Error:        "Borgmatic is not configured. Set LIZARD_BORGMATIC_CMD (e.g. 'docker exec -i borgmatic borgmatic info --json') or LIZARD_BORGMATIC_FILE.",
		}
	}

	stats, err := parseBorgmaticJSON(rawBytes)
	if err != nil {
		preview := string(rawBytes)
		if len(preview) > 120 {
			preview = preview[:120] + "..."
		}
		return BorgmaticStats{
			LastUpdated:  time.Now(),
			ConfigStatus: "JSON Parse Error",
			Error:        fmt.Sprintf("invalid JSON output: %v (received: %s)", err, preview),
		}
	}

	return stats
}

func parseBorgmaticJSON(data []byte) (BorgmaticStats, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return BorgmaticStats{
			LastUpdated:  time.Now(),
			ConfigStatus: "Empty Output",
			Error:        "received empty output from Borgmatic",
		}, nil
	}

	var repoList []rawRepoJSON
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &repoList); err != nil {
			return BorgmaticStats{}, err
		}
	} else {
		var single rawRepoJSON
		if err := json.Unmarshal([]byte(trimmed), &single); err != nil {
			return BorgmaticStats{}, err
		}
		repoList = append(repoList, single)
	}

	var repos []BorgRepoInfo
	var allArchives []BorgArchive

	for _, rawRepo := range repoList {
		loc := rawRepo.Repository.Location
		if loc == "" {
			loc = "default"
		}

		repo := BorgRepoInfo{
			Location: loc,
			ID:       rawRepo.Repository.ID,
		}

		for _, rawArch := range rawRepo.Archives {
			name := rawArch.Name
			if name == "" {
				name = rawArch.Archive
			}

			startTime, _ := parseFlexibleTime(rawArch.Start)
			endTime, _ := parseFlexibleTime(rawArch.End)

			duration := "N/A"
			if !startTime.IsZero() && !endTime.IsZero() && endTime.After(startTime) {
				dur := endTime.Sub(startTime)
				mins := int(dur.Minutes())
				secs := int(dur.Seconds()) % 60
				if mins > 0 {
					duration = fmt.Sprintf("%dm %ds", mins, secs)
				} else {
					duration = fmt.Sprintf("%ds", secs)
				}
			}

			timeStr := rawArch.Start
			if !startTime.IsZero() {
				timeStr = startTime.Format("2006-01-02 15:04:05")
			}

			arch := BorgArchive{
				Name:              name,
				Archive:           rawArch.Archive,
				Start:             startTime,
				StartTime:         timeStr,
				Duration:          duration,
				OriginalSize:      rawArch.Stats.OriginalSize,
				CompressedSize:    rawArch.Stats.CompressedSize,
				DeduplicatedSize:  rawArch.Stats.DeduplicatedSize,
				HumanOriginal:     FormatBytes(rawArch.Stats.OriginalSize),
				HumanCompressed:   FormatBytes(rawArch.Stats.CompressedSize),
				HumanDeduplicated: FormatBytes(rawArch.Stats.DeduplicatedSize),
				NFiles:            rawArch.Stats.NFiles,
				RepoLocation:      loc,
			}

			repo.Archives = append(repo.Archives, arch)
			allArchives = append(allArchives, arch)
			repo.TotalOriginal += arch.OriginalSize
			repo.TotalDeduplicated += arch.DeduplicatedSize
		}

		sort.Slice(repo.Archives, func(i, j int) bool {
			return repo.Archives[i].Start.After(repo.Archives[j].Start)
		})

		repo.TotalArchives = len(repo.Archives)
		repo.HumanOriginal = FormatBytes(repo.TotalOriginal)
		repo.HumanDeduplicated = FormatBytes(repo.TotalDeduplicated)
		repos = append(repos, repo)
	}

	sort.Slice(allArchives, func(i, j int) bool {
		return allArchives[i].Start.After(allArchives[j].Start)
	})

	return BorgmaticStats{
		Repositories: repos,
		AllArchives:  allArchives,
		TotalBackups: len(allArchives),
		LastUpdated:  time.Now(),
		ConfigStatus: "OK",
	}, nil
}

func parseFlexibleTime(val string) (time.Time, error) {
	if val == "" {
		return time.Time{}, nil
	}
	layouts := []string{
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05",
		time.RFC3339Nano,
		time.RFC3339,
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, val); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unable to parse time: %s", val)
}

func (b *BorgmaticCollector) getDemoStats() BorgmaticStats {
	now := time.Now()

	archives := []BorgArchive{
		{
			Name:              "server-daily-2026-11-14T02:00",
			Archive:           "server-daily-2026-11-14T02:00",
			Start:             now.Add(-18 * time.Hour),
			StartTime:         now.Add(-18 * time.Hour).Format("2006-01-02 15:04:05"),
			Duration:          "2m 14s",
			OriginalSize:      45 * 1024 * 1024 * 1024,
			CompressedSize:    31 * 1024 * 1024 * 1024,
			DeduplicatedSize:  420 * 1024 * 1024,
			HumanOriginal:     "45.00 GB",
			HumanCompressed:   "31.00 GB",
			HumanDeduplicated: "420.00 MB",
			NFiles:            124850,
			RepoLocation:      "/mnt/backup/borg-repo",
		},
		{
			Name:              "server-daily-2026-11-13T02:00",
			Archive:           "server-daily-2026-11-13T02:00",
			Start:             now.Add(-42 * time.Hour),
			StartTime:         now.Add(-42 * time.Hour).Format("2006-01-02 15:04:05"),
			Duration:          "1m 58s",
			OriginalSize:      44 * 1024 * 1024 * 1024,
			CompressedSize:    30 * 1024 * 1024 * 1024,
			DeduplicatedSize:  310 * 1024 * 1024,
			HumanOriginal:     "44.00 GB",
			HumanCompressed:   "30.00 GB",
			HumanDeduplicated: "310.00 MB",
			NFiles:            123920,
			RepoLocation:      "/mnt/backup/borg-repo",
		},
		{
			Name:              "server-daily-2026-11-12T02:00",
			Archive:           "server-daily-2026-11-12T02:00",
			Start:             now.Add(-66 * time.Hour),
			StartTime:         now.Add(-66 * time.Hour).Format("2006-01-02 15:04:05"),
			Duration:          "2m 45s",
			OriginalSize:      44 * 1024 * 1024 * 1024,
			CompressedSize:    30 * 1024 * 1024 * 1024,
			DeduplicatedSize:  850 * 1024 * 1024,
			HumanOriginal:     "44.00 GB",
			HumanCompressed:   "30.00 GB",
			HumanDeduplicated: "850.00 MB",
			NFiles:            123410,
			RepoLocation:      "/mnt/backup/borg-repo",
		},
		{
			Name:              "server-weekly-2026-11-08T03:00",
			Archive:           "server-weekly-2026-11-08T03:00",
			Start:             now.Add(-162 * time.Hour),
			StartTime:         now.Add(-162 * time.Hour).Format("2006-01-02 15:04:05"),
			Duration:          "5m 12s",
			OriginalSize:      43 * 1024 * 1024 * 1024,
			CompressedSize:    29 * 1024 * 1024 * 1024,
			DeduplicatedSize:  28 * 1024 * 1024 * 1024,
			HumanOriginal:     "43.00 GB",
			HumanCompressed:   "29.00 GB",
			HumanDeduplicated: "28.00 GB",
			NFiles:            121500,
			RepoLocation:      "/mnt/backup/borg-repo",
		},
	}

	repo := BorgRepoInfo{
		Location:          "/mnt/backup/borg-repo",
		ID:                "9d8f7e6a5b4c3d2e",
		Archives:          archives,
		TotalArchives:     len(archives),
		TotalOriginal:     176 * 1024 * 1024 * 1024,
		TotalDeduplicated: 29580 * 1024 * 1024,
		HumanOriginal:     "176.00 GB",
		HumanDeduplicated: "29.58 GB",
	}

	return BorgmaticStats{
		Repositories: []BorgRepoInfo{repo},
		AllArchives:  archives,
		TotalBackups: len(archives),
		LastUpdated:  now,
		ConfigStatus: "Demo Mode",
	}
}
