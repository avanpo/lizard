package main

import (
	"bufio"
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

// DiskStats contains disk space details and notable subdirectories for one disk.
type DiskStats struct {
	Path            string     `json:"path"`
	Device          string     `json:"device,omitempty"`
	FSType          string     `json:"fstype,omitempty"`
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

// SystemMetrics encapsulates CPU, Memory, and per-Disk stats with error states.
type SystemMetrics struct {
	Timestamp  time.Time   `json:"timestamp"`
	CPUPercent float64     `json:"cpu_percent"`
	CPUError   string      `json:"cpu_error,omitempty"`
	Memory     MemoryStats `json:"memory"`
	Disks      []DiskStats `json:"disks"`
	Uptime     string      `json:"uptime"`
	HostName   string      `json:"hostname"`
}

type cachedDiskDirs struct {
	dirs      []DirUsage
	warning   string
	permNotes []string
}

// SystemCollector collects host /proc and per-disk metrics.
type SystemCollector struct {
	hostRoot         string
	diskDirs         []string
	topDirLimit      int
	diskScanInterval time.Duration

	mu           sync.RWMutex
	prevCPUTotal uint64
	prevCPUIdle  uint64
	hasPrevCPU   bool
	cachedDisks  map[string]cachedDiskDirs
	lastDiskScan time.Time
	scanning     bool
	demoMode     bool
}

// NewSystemCollector initializes a new SystemCollector.
func NewSystemCollector(hostRoot string, diskDirs []string, topDirLimit int, diskScanInterval time.Duration, demo bool) *SystemCollector {
	return &SystemCollector{
		hostRoot:         hostRoot,
		diskDirs:         diskDirs,
		topDirLimit:      topDirLimit,
		diskScanInterval: diskScanInterval,
		cachedDisks:      make(map[string]cachedDiskDirs),
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
	disks := c.readDisksUsage()
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
		Disks:      disks,
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

func (c *SystemCollector) resolveTargets() []discoveredMount {
	if len(c.diskDirs) > 0 {
		var targets []discoveredMount
		for _, d := range c.diskDirs {
			display := d
			if c.hostRoot != "" && c.hostRoot != "/" && strings.HasPrefix(d, c.hostRoot) {
				stripped := strings.TrimPrefix(d, c.hostRoot)
				if stripped == "" {
					stripped = "/"
				}
				display = stripped
			}
			targets = append(targets, discoveredMount{
				DisplayPath: display,
				ScanPath:    d,
			})
		}
		return targets
	}

	mounts := discoverMounts(c.hostRoot)
	if len(mounts) > 0 {
		return mounts
	}

	// Fallback if /proc/mounts unavailable
	scan := "/"
	if c.hostRoot != "" {
		scan = c.hostRoot
	}
	return []discoveredMount{{DisplayPath: "/", ScanPath: scan}}
}

// readDisksUsage queries filesystem stats for all discovered/configured disks and refreshes directory trees.
func (c *SystemCollector) readDisksUsage() []DiskStats {
	targets := c.resolveTargets()

	type statResult struct {
		mount discoveredMount
		total uint64
		used  uint64
		free  uint64
		pct   float64
		err   string
	}

	stats := make([]statResult, len(targets))
	for i, t := range targets {
		total, free, _, err := getDiskUsageStat(t.ScanPath)
		var used uint64
		var usedPercent float64
		var diskErr string

		if err != nil {
			if os.IsPermission(err) {
				diskErr = fmt.Sprintf("permission denied reading filesystem stats for %s", t.ScanPath)
			} else if os.IsNotExist(err) {
				diskErr = fmt.Sprintf("target directory %s does not exist", t.ScanPath)
			} else {
				diskErr = fmt.Sprintf("failed reading disk stat for %s: %v", t.ScanPath, err)
			}
		} else if total > 0 {
			if total >= free {
				used = total - free
			}
			usedPercent = (float64(used) / float64(total)) * 100.0
		}

		stats[i] = statResult{
			mount: t,
			total: total,
			used:  used,
			free:  free,
			pct:   math.Round(usedPercent*10) / 10,
			err:   diskErr,
		}
	}

	// Check if directory scan cache needs refresh
	c.mu.Lock()
	needScan := len(c.cachedDisks) == 0 || time.Since(c.lastDiskScan) > c.diskScanInterval
	firstScan := len(c.cachedDisks) == 0
	if needScan && !c.scanning {
		c.scanning = true
		c.lastDiskScan = time.Now()
		c.mu.Unlock()

		runScan := func() {
			updated := make(map[string]cachedDiskDirs, len(stats))
			for _, st := range stats {
				if st.err != "" {
					continue
				}
				dirs, warn, notes := c.scanInterestingDirs(st.mount, st.total)
				updated[st.mount.DisplayPath] = cachedDiskDirs{
					dirs:      dirs,
					warning:   warn,
					permNotes: notes,
				}
			}
			c.mu.Lock()
			c.cachedDisks = updated
			c.lastDiskScan = time.Now()
			c.scanning = false
			c.mu.Unlock()
		}

		if firstScan {
			runScan()
		} else {
			go runScan()
		}
	} else {
		c.mu.Unlock()
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	results := make([]DiskStats, len(stats))
	for i, st := range stats {
		cached := c.cachedDisks[st.mount.DisplayPath]
		results[i] = DiskStats{
			Path:            st.mount.DisplayPath,
			Device:          st.mount.Device,
			FSType:          st.mount.FSType,
			Total:           st.total,
			Used:            st.used,
			Free:            st.free,
			UsedPercent:     st.pct,
			HumanTotal:      FormatBytes(st.total),
			HumanUsed:       FormatBytes(st.used),
			HumanFree:       FormatBytes(st.free),
			TopDirs:         cached.dirs,
			Error:           st.err,
			TopDirsWarning:  cached.warning,
			PermissionNotes: cached.permNotes,
		}
	}
	return results
}

type dirNode struct {
	displayPath string
	totalSize   uint64
	selfSize    uint64
	fileCount   int64
	depth       int
	children    []*dirNode
}

// scanInterestingDirs walks a single disk (staying on its device ID) and selects the most notable directories.
func (c *SystemCollector) scanInterestingDirs(m discoveredMount, totalDiskBytes uint64) ([]DirUsage, string, []string) {
	rootDev, hasDev := getDevID(m.ScanPath)

	var permNotes []string
	var skippedCount int

	skipRootDirs := map[string]bool{
		"proc": true, "sys": true, "dev": true, "run": true,
	}

	const maxWalkDepth = 6

	var buildTree func(scanPath, displayPath string, depth int) *dirNode
	buildTree = func(scanPath, displayPath string, depth int) *dirNode {
		entries, err := os.ReadDir(scanPath)
		if err != nil {
			if os.IsPermission(err) {
				skippedCount++
				if len(permNotes) < 5 {
					permNotes = append(permNotes, fmt.Sprintf("permission denied on %s", displayPath))
				}
			}
			return nil
		}

		node := &dirNode{
			displayPath: displayPath,
			depth:       depth,
		}

		for _, entry := range entries {
			name := entry.Name()
			if depth == 0 && m.DisplayPath == "/" && skipRootDirs[name] {
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}

			childScan := filepath.Join(scanPath, name)
			childDisplay := filepath.Join(displayPath, name)
			if displayPath == "/" {
				childDisplay = "/" + name
			}

			if entry.IsDir() {
				// Stay on the same filesystem so mounted sub-disks aren't double-counted
				if hasDev {
					if childDev, ok := getDevID(childScan); ok && childDev != rootDev {
						continue
					}
				}

				if depth+1 < maxWalkDepth {
					childNode := buildTree(childScan, childDisplay, depth+1)
					if childNode != nil && childNode.totalSize > 0 {
						node.children = append(node.children, childNode)
						node.totalSize += childNode.totalSize
						node.fileCount += childNode.fileCount
					}
				} else {
					sz, fc, permErr := sumSubtree(childScan, rootDev, hasDev)
					if permErr {
						skippedCount++
					}
					if sz > 0 {
						node.children = append(node.children, &dirNode{
							displayPath: childDisplay,
							totalSize:   sz,
							selfSize:    sz,
							fileCount:   fc,
							depth:       depth + 1,
						})
						node.totalSize += sz
						node.fileCount += fc
					}
				}
			} else {
				info, err := entry.Info()
				if err == nil {
					sz := uint64(info.Size())
					node.selfSize += sz
					node.totalSize += sz
					node.fileCount++
				}
			}
		}

		return node
	}

	rootNode := buildTree(m.ScanPath, m.DisplayPath, 0)
	if rootNode == nil {
		return nil, fmt.Sprintf("unable to read directory %s", m.ScanPath), permNotes
	}

	selected := selectInterestingNodes(rootNode, c.topDirLimit)

	results := make([]DirUsage, 0, len(selected))
	for _, n := range selected {
		var pct float64
		if totalDiskBytes > 0 {
			pct = (float64(n.totalSize) / float64(totalDiskBytes)) * 100.0
		}
		results = append(results, DirUsage{
			Path:      n.displayPath,
			SizeBytes: n.totalSize,
			HumanSize: FormatBytes(n.totalSize),
			Percent:   math.Round(pct*10) / 10,
			FileCount: n.fileCount,
		})
	}

	var warning string
	if skippedCount > 0 {
		warning = fmt.Sprintf("%d restricted directories were partially or fully skipped due to permissions", skippedCount)
	}
	return results, warning, permNotes
}

// sumSubtree sums all regular files beneath dir while staying on rootDev.
func sumSubtree(dir string, rootDev uint64, hasDev bool) (uint64, int64, bool) {
	var totalSize uint64
	var fileCount int64
	var hadPermErr bool

	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsPermission(err) {
				hadPermErr = true
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if hasDev {
				if dev, ok := getDevID(p); ok && dev != rootDev {
					return fs.SkipDir
				}
			}
			return nil
		}
		info, err := d.Info()
		if err == nil {
			totalSize += uint64(info.Size())
			fileCount++
		}
		return nil
	})

	return totalSize, fileCount, hadPermErr
}

// selectInterestingNodes picks the most informative directories using top-down dominance drill-down:
// - If a parent is mostly a wrapper around one huge child (e.g. /var -> /var/log -> /var/log/caddy),
//   it drills down to the child.
// - If a parent has many distributed children (e.g. /data/photos/2018..2026), it keeps the parent rolled up.
func selectInterestingNodes(root *dirNode, limit int) []*dirNode {
	if limit <= 0 {
		limit = 10
	}

	candidates := make([]*dirNode, len(root.children))
	copy(candidates, root.children)

	var kept []*dirNode

	for len(candidates) > 0 && len(kept) < limit {
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].totalSize > candidates[j].totalSize
		})

		top := candidates[0]
		candidates = candidates[1:]

		if shouldDrillDown(top) {
			candidates = append(candidates, top.children...)
			continue
		}

		kept = append(kept, top)
	}

	sort.Slice(kept, func(i, j int) bool {
		return kept[i].totalSize > kept[j].totalSize
	})
	return kept
}

func shouldDrillDown(n *dirNode) bool {
	if len(n.children) == 0 || n.totalSize == 0 {
		return false
	}

	var maxChildSize uint64
	var childrenSum uint64
	for _, ch := range n.children {
		childrenSum += ch.totalSize
		if ch.totalSize > maxChildSize {
			maxChildSize = ch.totalSize
		}
	}

	maxShare := float64(maxChildSize) / float64(n.totalSize)
	childrenShare := float64(childrenSum) / float64(n.totalSize)

	// Single child dominates this directory (e.g. /var -> /var/log -> /var/log/app)
	if maxShare >= 0.70 {
		return true
	}

	// Shallow umbrella directory (/var, /home, /mnt, /srv, /opt, etc.) whose size is mostly in a few subdirs,
	// rather than a collection of many small subdirs like photos/2018..2026.
	isCollection := len(n.children) >= 8 && maxShare < 0.40
	if n.depth <= 2 && childrenShare >= 0.85 && !isCollection {
		return true
	}

	return false
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

// collectDemo returns synthetic system data for multiple disks.
func (c *SystemCollector) collectDemo(now time.Time, hostname string) SystemMetrics {
	totalMem := uint64(32 * 1024 * 1024 * 1024)
	usedMem := uint64(142 * 1024 * 1024 * 1024 / 10)

	disks := []DiskStats{
		{
			Path:        "/",
			Device:      "/dev/nvme0n1p2",
			FSType:      "ext4",
			Total:       256 * 1024 * 1024 * 1024,
			Used:        118 * 1024 * 1024 * 1024,
			Free:        138 * 1024 * 1024 * 1024,
			UsedPercent: 46.1,
			HumanTotal:  "256.00 GB",
			HumanUsed:   "118.00 GB",
			HumanFree:   "138.00 GB",
			TopDirs: []DirUsage{
				{Path: "/var/lib/docker", SizeBytes: 64 * 1024 * 1024 * 1024, HumanSize: "64.00 GB", Percent: 25.0, FileCount: 64230},
				{Path: "/var/log/caddy", SizeBytes: 24 * 1024 * 1024 * 1024, HumanSize: "24.00 GB", Percent: 9.4, FileCount: 1420},
				{Path: "/usr", SizeBytes: 14 * 1024 * 1024 * 1024, HumanSize: "14.00 GB", Percent: 5.5, FileCount: 89400},
			},
		},
		{
			Path:        "/mnt/storage",
			Device:      "/dev/sda1",
			FSType:      "ext4",
			Total:       4 * 1024 * 1024 * 1024 * 1024,
			Used:        2850 * 1024 * 1024 * 1024,
			Free:        1246 * 1024 * 1024 * 1024,
			UsedPercent: 69.6,
			HumanTotal:  "4.00 TB",
			HumanUsed:   "2.78 TB",
			HumanFree:   "1.22 TB",
			TopDirs: []DirUsage{
				{Path: "/mnt/storage/photos", SizeBytes: 1200 * 1024 * 1024 * 1024, HumanSize: "1.17 TB", Percent: 29.3, FileCount: 184200},
				{Path: "/mnt/storage/syncthing/projects", SizeBytes: 980 * 1024 * 1024 * 1024, HumanSize: "980.00 GB", Percent: 23.9, FileCount: 41200},
				{Path: "/mnt/storage/media", SizeBytes: 620 * 1024 * 1024 * 1024, HumanSize: "620.00 GB", Percent: 15.1, FileCount: 3100},
			},
		},
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
		Disks:    disks,
		Uptime:   "18d 6h 32m",
		HostName: hostname + " (demo)",
	}
}
