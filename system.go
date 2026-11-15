package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MemoryStats contains memory usage details.
type MemoryStats struct {
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Free        uint64  `json:"free"`
	Available   uint64  `json:"available"`
	UsedPercent float64 `json:"used_percent"`
	HumanTotal  string  `json:"human_total"`
	HumanUsed   string  `json:"human_used"`
	HumanFree   string  `json:"human_free"`
	Error       string  `json:"error,omitempty"`
}

// DirUsage represents disk usage for a specific directory.
type DirUsage struct {
	Path      string  `json:"path"`
	SizeBytes uint64  `json:"size_bytes"`
	HumanSize string  `json:"human_size"`
	Percent   float64 `json:"percent"`
	FileCount int64   `json:"file_count"`
}

// DiskStats contains disk space details and top subdirectories.
type DiskStats struct {
	Path            string     `json:"path"`
	Total           uint64     `json:"total"`
	Used            uint64     `json:"used"`
	Free            uint64     `json:"free"`
	UsedPercent     float64    `json:"used_percent"`
	HumanTotal      string     `json:"human_total"`
	HumanUsed       string     `json:"human_used"`
	HumanFree       string     `json:"human_free"`
	TopDirs         []DirUsage `json:"top_dirs"`
	Error           string     `json:"error,omitempty"`
	TopDirsWarning  string     `json:"top_dirs_warning,omitempty"`
	PermissionNotes []string   `json:"permission_notes,omitempty"`
}

// SystemMetrics encapsulates CPU, Memory, and Disk stats with error states.
type SystemMetrics struct {
	Timestamp  time.Time   `json:"timestamp"`
	CPUPercent float64     `json:"cpu_percent"`
	CPUError   string      `json:"cpu_error,omitempty"`
	Memory     MemoryStats `json:"memory"`
	Disk       DiskStats   `json:"disk"`
	Uptime     string      `json:"uptime"`
	HostName   string      `json:"hostname"`
}

// SystemCollector collects host /proc and disk metrics.
type SystemCollector struct {
	hostRoot         string
	diskDirs         []string
	topDirLimit      int
	diskScanInterval time.Duration

	mu              sync.RWMutex
	prevCPUTotal    uint64
	prevCPUIdle     uint64
	hasPrevCPU      bool
	cachedTopDirs   []DirUsage
	cachedWarnings  string
	cachedPermNotes []string
	lastDiskScan    time.Time
	demoMode        bool
}

// NewSystemCollector initializes a new SystemCollector.
func NewSystemCollector(hostRoot string, diskDirs []string, topDirLimit int, diskScanInterval time.Duration, demo bool) *SystemCollector {
	return &SystemCollector{
		hostRoot:         hostRoot,
		diskDirs:         diskDirs,
		topDirLimit:      topDirLimit,
		diskScanInterval: diskScanInterval,
		demoMode:         demo,
	}
}

// Collect returns the latest system metrics along with any errors encountered.
func (c *SystemCollector) Collect() SystemMetrics {
	now := time.Now()
	hostname, _ := os.Hostname()

	if c.demoMode {
		return c.collectDemo(now, hostname)
	}

	cpuPercent, cpuErr := c.readCPUUsage()
	memStats := c.readMemoryUsage()
	diskStats := c.readDiskUsage()
	uptime := c.readUptime()

	cpuErrStr := ""
	if cpuErr != nil {
		cpuErrStr = cpuErr.Error()
	}

	return SystemMetrics{
		Timestamp:  now,
		CPUPercent: math.Round(cpuPercent*10) / 10,
		CPUError:   cpuErrStr,
		Memory:     memStats,
		Disk:       diskStats,
		Uptime:     uptime,
		HostName:   hostname,
	}
}

// procPath resolves a /proc path respecting hostRoot if mounted.
func (c *SystemCollector) procPath(filename string) (string, error) {
	if c.hostRoot != "" && c.hostRoot != "/" {
		candidate := filepath.Join(c.hostRoot, "proc", filename)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	defaultPath := filepath.Join("/proc", filename)
	if _, err := os.Stat(defaultPath); err != nil {
		return defaultPath, err
	}
	return defaultPath, nil
}

// readCPUUsage parses /proc/stat to calculate CPU percentage between calls.
func (c *SystemCollector) readCPUUsage() (float64, error) {
	path, statErr := c.procPath("stat")
	if statErr != nil {
		return 0.0, fmt.Errorf("cannot access CPU stats at %s: %w", path, statErr)
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsPermission(err) {
			return 0.0, fmt.Errorf("permission denied reading %s: ensure container has access to host /proc", path)
		}
		return 0.0, fmt.Errorf("failed to open %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return 0.0, fmt.Errorf("empty or unreadable %s", path)
	}
	line := scanner.Text()
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0.0, fmt.Errorf("unexpected format in %s (expected 'cpu' header)", path)
	}

	var total uint64
	var idle uint64
	for i := 1; i < len(fields); i++ {
		val, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			continue
		}
		total += val
		if i == 4 || i == 5 { // idle + iowait
			idle += val
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.hasPrevCPU {
		c.prevCPUTotal = total
		c.prevCPUIdle = idle
		c.hasPrevCPU = true
		return 0.0, nil
	}

	totalDelta := total - c.prevCPUTotal
	idleDelta := idle - c.prevCPUIdle

	c.prevCPUTotal = total
	c.prevCPUIdle = idle

	if totalDelta == 0 {
		return 0.0, nil
	}

	used := float64(totalDelta - idleDelta)
	percent := (used / float64(totalDelta)) * 100.0
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return percent, nil
}

// readMemoryUsage parses /proc/meminfo.
func (c *SystemCollector) readMemoryUsage() MemoryStats {
	path, statErr := c.procPath("meminfo")
	if statErr != nil {
		return MemoryStats{
			Error: fmt.Sprintf("cannot access memory stats at %s: %v", path, statErr),
		}
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsPermission(err) {
			return MemoryStats{
				Error: fmt.Sprintf("permission denied reading %s", path),
			}
		}
		return MemoryStats{
			Error: fmt.Sprintf("failed opening %s: %v", path, err),
		}
	}
	defer f.Close()

	var memTotal, memFree, memAvail, buffers, cached uint64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Split(line, ":")
		if len(parts) < 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		valParts := strings.Fields(parts[1])
		if len(valParts) == 0 {
			continue
		}
		kb, _ := strconv.ParseUint(valParts[0], 10, 64)
		bytesVal := kb * 1024

		switch key {
		case "MemTotal":
			memTotal = bytesVal
		case "MemFree":
			memFree = bytesVal
		case "MemAvailable":
			memAvail = bytesVal
		case "Buffers":
			buffers = bytesVal
		case "Cached":
			cached = bytesVal
		}
	}

	if memTotal == 0 {
		return MemoryStats{
			Error: fmt.Sprintf("invalid or empty memory data read from %s", path),
		}
	}

	var used uint64
	if memAvail > 0 && memTotal >= memAvail {
		used = memTotal - memAvail
	} else if memTotal >= (memFree + buffers + cached) {
		used = memTotal - memFree - buffers - cached
	}

	var usedPercent float64
	if memTotal > 0 {
		usedPercent = (float64(used) / float64(memTotal)) * 100.0
	}

	return MemoryStats{
		Total:       memTotal,
		Used:        used,
		Free:        memFree,
		Available:   memAvail,
		UsedPercent: math.Round(usedPercent*10) / 10,
		HumanTotal:  FormatBytes(memTotal),
		HumanUsed:   FormatBytes(used),
		HumanFree:   FormatBytes(memAvail),
	}
}

// readDiskUsage queries filesystem stat and triggers directory scans.
func (c *SystemCollector) readDiskUsage() DiskStats {
	targetDir := "/"
	if len(c.diskDirs) > 0 {
		targetDir = c.diskDirs[0]
	}

	checkDir := targetDir
	if c.hostRoot != "" && c.hostRoot != "/" {
		checkDir = c.hostRoot
	}

	total, free, _, err := getDiskUsageStat(checkDir)
	var used uint64
	var usedPercent float64
	var diskErr string

	if err != nil {
		if os.IsPermission(err) {
			diskErr = fmt.Sprintf("permission denied reading filesystem stats for %s", checkDir)
		} else if os.IsNotExist(err) {
			diskErr = fmt.Sprintf("target directory %s does not exist", checkDir)
		} else {
			diskErr = fmt.Sprintf("failed reading disk stat for %s: %v", checkDir, err)
		}
	} else if total > 0 {
		if total >= free {
			used = total - free
		}
		usedPercent = (float64(used) / float64(total)) * 100.0
	}

	topDirs, topWarning, permNotes := c.getOrUpdateTopDirs(checkDir, total)

	return DiskStats{
		Path:            targetDir,
		Total:           total,
		Used:            used,
		Free:            free,
		UsedPercent:     math.Round(usedPercent*10) / 10,
		HumanTotal:      FormatBytes(total),
		HumanUsed:       FormatBytes(used),
		HumanFree:       FormatBytes(free),
		TopDirs:         topDirs,
		Error:           diskErr,
		TopDirsWarning:  topWarning,
		PermissionNotes: permNotes,
	}
}

// getOrUpdateTopDirs returns cached top directories or triggers background refresh.
func (c *SystemCollector) getOrUpdateTopDirs(rootDir string, totalDiskBytes uint64) ([]DirUsage, string, []string) {
	c.mu.Lock()
	now := time.Now()
	if c.cachedTopDirs == nil || now.Sub(c.lastDiskScan) > c.diskScanInterval {
		c.lastDiskScan = now
		c.mu.Unlock()

		if c.cachedTopDirs == nil {
			dirs, warning, permNotes := c.scanTopDirs(rootDir, totalDiskBytes)
			c.mu.Lock()
			c.cachedTopDirs = dirs
			c.cachedWarnings = warning
			c.cachedPermNotes = permNotes
			c.mu.Unlock()
			return dirs, warning, permNotes
		}

		go func() {
			dirs, warning, permNotes := c.scanTopDirs(rootDir, totalDiskBytes)
			c.mu.Lock()
			c.cachedTopDirs = dirs
			c.cachedWarnings = warning
			c.cachedPermNotes = permNotes
			c.mu.Unlock()
		}()
	} else {
		c.mu.Unlock()
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cachedTopDirs, c.cachedWarnings, c.cachedPermNotes
}

// scanTopDirs scans subdirectories of the target and computes their sizes.
func (c *SystemCollector) scanTopDirs(rootDir string, totalDiskBytes uint64) ([]DirUsage, string, []string) {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		if os.IsPermission(err) {
			return nil, fmt.Sprintf("permission denied reading directory %s: container needs root privileges to inspect host files", rootDir), nil
		}
		return nil, fmt.Sprintf("unable to read directory %s: %v", rootDir, err), nil
	}

	skipDirs := map[string]bool{
		"proc": true, "sys": true, "dev": true, "run": true,
	}

	var results []DirUsage
	var permNotes []string
	var skippedCount int

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if skipDirs[name] {
			continue
		}

		subPath := filepath.Join(rootDir, name)
		size, fileCount, permErr := calculateDirSize(subPath, 4)
		if permErr != nil {
			skippedCount++
			if len(permNotes) < 5 {
				permNotes = append(permNotes, fmt.Sprintf("permission denied on %s", name))
			}
		}

		if size > 0 {
			var pct float64
			if totalDiskBytes > 0 {
				pct = (float64(size) / float64(totalDiskBytes)) * 100.0
			}
			displayName := "/" + name
			if rootDir != "/" && rootDir != c.hostRoot {
				displayName = subPath
			}
			results = append(results, DirUsage{
				Path:      displayName,
				SizeBytes: size,
				HumanSize: FormatBytes(size),
				Percent:   math.Round(pct*10) / 10,
				FileCount: fileCount,
			})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].SizeBytes > results[j].SizeBytes
	})

	if len(results) > c.topDirLimit {
		results = results[:c.topDirLimit]
	}

	var warning string
	if skippedCount > 0 {
		warning = fmt.Sprintf("%d restricted directories were partially or fully skipped due to permissions", skippedCount)
	}

	return results, warning, permNotes
}

// calculateDirSize traverses a directory up to maxDepth and sums file sizes.
func calculateDirSize(dir string, maxDepth int) (uint64, int64, error) {
	var totalSize uint64
	var fileCount int64
	var hadPermErr bool

	baseDepth := strings.Count(filepath.Clean(dir), string(os.PathSeparator))

	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrPermission) || os.IsPermission(err) {
				hadPermErr = true
			}
			return nil
		}
		currentDepth := strings.Count(filepath.Clean(p), string(os.PathSeparator))
		if currentDepth-baseDepth > maxDepth {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if !d.IsDir() {
			info, err := d.Info()
			if err == nil {
				totalSize += uint64(info.Size())
				fileCount++
			}
		}
		return nil
	})

	if hadPermErr {
		return totalSize, fileCount, errors.New("partial permission denied")
	}
	return totalSize, fileCount, nil
}

// readUptime returns formatted system uptime.
func (c *SystemCollector) readUptime() string {
	path, err := c.procPath("uptime")
	if err != nil {
		return "N/A"
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "N/A"
	}
	parts := strings.Fields(string(content))
	if len(parts) == 0 {
		return "N/A"
	}
	secs, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return "N/A"
	}

	dur := time.Duration(secs) * time.Second
	days := int(dur.Hours()) / 24
	hours := int(dur.Hours()) % 24
	mins := int(dur.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	return fmt.Sprintf("%dh %dm", hours, mins)
}

// FormatBytes formats a raw byte count into human-readable B, KB, MB, GB, TB.
func FormatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	return fmt.Sprintf("%.2f %s", float64(b)/float64(div), units[exp])
}

// collectDemo returns synthetic system data for demonstration or local dev.
func (c *SystemCollector) collectDemo(now time.Time, hostname string) SystemMetrics {
	totalMem := uint64(32 * 1024 * 1024 * 1024)
	usedMem := uint64(142 * 1024 * 1024 * 1024 / 10)
	totalDisk := uint64(512 * 1024 * 1024 * 1024)
	usedDisk := uint64(238 * 1024 * 1024 * 1024)

	topDirs := []DirUsage{
		{Path: "/var/lib/docker", SizeBytes: 85 * 1024 * 1024 * 1024, HumanSize: "85.00 GB", Percent: 16.6, FileCount: 64230},
		{Path: "/home", SizeBytes: 72 * 1024 * 1024 * 1024, HumanSize: "72.00 GB", Percent: 14.1, FileCount: 142100},
		{Path: "/mnt/backups", SizeBytes: 52 * 1024 * 1024 * 1024, HumanSize: "52.00 GB", Percent: 10.2, FileCount: 38200},
		{Path: "/usr", SizeBytes: 14 * 1024 * 1024 * 1024, HumanSize: "14.00 GB", Percent: 2.7, FileCount: 89400},
		{Path: "/var/log", SizeBytes: 4 * 1024 * 1024 * 1024, HumanSize: "4.00 GB", Percent: 0.8, FileCount: 5410},
	}

	return SystemMetrics{
		Timestamp:  now,
		CPUPercent: 18.4,
		Memory: MemoryStats{
			Total:       totalMem,
			Used:        usedMem,
			Free:        totalMem - usedMem,
			Available:   totalMem - usedMem,
			UsedPercent: 44.4,
			HumanTotal:  FormatBytes(totalMem),
			HumanUsed:   FormatBytes(usedMem),
			HumanFree:   FormatBytes(totalMem - usedMem),
		},
		Disk: DiskStats{
			Path:        "/",
			Total:       totalDisk,
			Used:        usedDisk,
			Free:        totalDisk - usedDisk,
			UsedPercent: 46.5,
			HumanTotal:  FormatBytes(totalDisk),
			HumanUsed:   FormatBytes(usedDisk),
			HumanFree:   FormatBytes(totalDisk - usedDisk),
			TopDirs:     topDirs,
		},
		Uptime:   "18d 6h 32m",
		HostName: hostname + " (demo)",
	}
}
