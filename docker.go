package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ContainerInfo represents summary and resource stats for a Docker container.
type ContainerInfo struct {
	ID            string  `json:"id"`
	FullID        string  `json:"full_id"`
	Name          string  `json:"name"`
	Image         string  `json:"image"`
	State         string  `json:"state"`
	Status        string  `json:"status"`
	Created       int64   `json:"created"`
	Uptime        string  `json:"uptime"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemUsage      uint64  `json:"mem_usage"`
	MemLimit      uint64  `json:"mem_limit"`
	MemPercent    float64 `json:"mem_percent"`
	HumanMemUsage string  `json:"human_mem_usage"`
	HumanMemLimit string  `json:"human_mem_limit"`
}

// DockerCollector communicates with the local Docker daemon via unix socket.
type DockerCollector struct {
	socketPath string
	client     *http.Client
	demoMode   bool
}

// NewDockerCollector creates a new DockerCollector.
func NewDockerCollector(socketPath string, demo bool) *DockerCollector {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socketPath)
			},
		},
		Timeout: 5 * time.Second,
	}

	return &DockerCollector{
		socketPath: socketPath,
		client:     client,
		demoMode:   demo,
	}
}

// rawContainerJSON mirrors the Docker Engine container list JSON item.
type rawContainerJSON struct {
	ID      string   `json:"Id"`
	Names   []string `json:"Names"`
	Image   string   `json:"Image"`
	State   string   `json:"State"`
	Status  string   `json:"Status"`
	Created int64    `json:"Created"`
}

// rawStatsJSON mirrors Docker Engine container stats JSON.
type rawStatsJSON struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage  uint64   `json:"total_usage"`
			PercpuUsage []uint64 `json:"percpu_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs     uint32 `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage  uint64   `json:"total_usage"`
			PercpuUsage []uint64 `json:"percpu_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
}

// Collect queries Docker for all containers and their resource statistics.
func (d *DockerCollector) Collect() ([]ContainerInfo, error) {
	if d.demoMode {
		return d.getDemoContainers(), nil
	}

	fi, err := os.Stat(d.socketPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("docker socket not found at %s. Ensure `-v /var/run/docker.sock:%s` is mounted in docker-compose.yml", d.socketPath, d.socketPath)
		}
		if os.IsPermission(err) {
			return nil, fmt.Errorf("permission denied accessing %s. The container must run as user '0:0' or belong to host docker GID", d.socketPath)
		}
		return nil, fmt.Errorf("cannot stat docker socket %s: %w", d.socketPath, err)
	}

	if fi.Mode()&os.ModeSocket == 0 {
		return nil, fmt.Errorf("path %s exists but is not a unix domain socket", d.socketPath)
	}

	resp, err := d.client.Get("http://docker/containers/json?all=1")
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "permission denied") {
			return nil, fmt.Errorf("permission denied connecting to %s. Ensure the container has root access or group permissions", d.socketPath)
		}
		if strings.Contains(errStr, "connection refused") {
			return nil, fmt.Errorf("connection refused at %s. Docker daemon may be stopped or restarting", d.socketPath)
		}
		return nil, fmt.Errorf("failed connecting to Docker API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("docker API returned status %d (Access Denied)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code from docker API: %d", resp.StatusCode)
	}

	var rawList []rawContainerJSON
	if err := json.NewDecoder(resp.Body).Decode(&rawList); err != nil {
		return nil, fmt.Errorf("failed to decode containers list: %w", err)
	}

	results := make([]ContainerInfo, len(rawList))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)

	for i, c := range rawList {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		shortID := c.ID
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}

		uptimeStr := c.Status
		if c.Created > 0 {
			createdTime := time.Unix(c.Created, 0)
			uptimeStr = formatDuration(time.Since(createdTime))
		}

		results[i] = ContainerInfo{
			ID:            shortID,
			FullID:        c.ID,
			Name:          name,
			Image:         c.Image,
			State:         c.State,
			Status:        c.Status,
			Created:       c.Created,
			Uptime:        uptimeStr,
			HumanMemUsage: "0 B",
			HumanMemLimit: "0 B",
		}

		if c.State == "running" {
			wg.Add(1)
			go func(idx int, containerID string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				cpuPct, memUsage, memLimit, memPct := d.queryContainerStats(containerID)
				results[idx].CPUPercent = cpuPct
				results[idx].MemUsage = memUsage
				results[idx].MemLimit = memLimit
				results[idx].MemPercent = memPct
				results[idx].HumanMemUsage = FormatBytes(memUsage)
				results[idx].HumanMemLimit = FormatBytes(memLimit)
			}(i, c.ID)
		}
	}

	wg.Wait()
	return results, nil
}

// queryContainerStats queries CPU and memory for a single container.
func (d *DockerCollector) queryContainerStats(containerID string) (float64, uint64, uint64, float64) {
	url := fmt.Sprintf("http://docker/containers/%s/stats?stream=false", containerID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, 0, 0, 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := d.client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return 0, 0, 0, 0
	}
	defer resp.Body.Close()

	var stats rawStatsJSON
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return 0, 0, 0, 0
	}

	var cpuPercent float64
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage) - float64(stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stats.CPUStats.SystemCPUUsage) - float64(stats.PreCPUStats.SystemCPUUsage)
	onlineCPUs := float64(stats.CPUStats.OnlineCPUs)
	if onlineCPUs == 0 {
		onlineCPUs = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if onlineCPUs == 0 {
		onlineCPUs = 1.0
	}

	if systemDelta > 0.0 && cpuDelta > 0.0 {
		cpuPercent = (cpuDelta / systemDelta) * onlineCPUs * 100.0
		cpuPercent = math.Round(cpuPercent*100) / 100
	}

	memUsage := stats.MemoryStats.Usage
	if cache, ok := stats.MemoryStats.Stats["cache"]; ok && memUsage > cache {
		memUsage -= cache
	} else if inactiveFile, ok := stats.MemoryStats.Stats["inactive_file"]; ok && memUsage > inactiveFile {
		memUsage -= inactiveFile
	}

	memLimit := stats.MemoryStats.Limit
	var memPercent float64
	if memLimit > 0 {
		memPercent = (float64(memUsage) / float64(memLimit)) * 100.0
		memPercent = math.Round(memPercent*10) / 10
	}

	return cpuPercent, memUsage, memLimit, memPercent
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}

func (d *DockerCollector) getDemoContainers() []ContainerInfo {
	return []ContainerInfo{
		{
			ID:            "a1b2c3d4e5f6",
			FullID:        "a1b2c3d4e5f6789012345678",
			Name:          "borgmatic",
			Image:         "b3vis/borgmatic:latest",
			State:         "running",
			Status:        "Up 4 days",
			Created:       time.Now().Add(-4 * 24 * time.Hour).Unix(),
			Uptime:        "4d 1h",
			CPUPercent:    0.2,
			MemUsage:      124 * 1024 * 1024,
			MemLimit:      4 * 1024 * 1024 * 1024,
			MemPercent:    3.0,
			HumanMemUsage: "124.00 MB",
			HumanMemLimit: "4.00 GB",
		},
		{
			ID:            "b2c3d4e5f6a1",
			FullID:        "b2c3d4e5f6a1789012345678",
			Name:          "syncthing",
			Image:         "syncthing/syncthing:latest",
			State:         "running",
			Status:        "Up 7 days",
			Created:       time.Now().Add(-7 * 24 * time.Hour).Unix(),
			Uptime:        "7d 12h",
			CPUPercent:    1.8,
			MemUsage:      215 * 1024 * 1024,
			MemLimit:      4 * 1024 * 1024 * 1024,
			MemPercent:    5.2,
			HumanMemUsage: "215.00 MB",
			HumanMemLimit: "4.00 GB",
		},
		{
			ID:            "c3d4e5f6a1b2",
			FullID:        "c3d4e5f6a1b2789012345678",
			Name:          "reverse-proxy",
			Image:         "traefik:v3.1",
			State:         "running",
			Status:        "Up 14 days",
			Created:       time.Now().Add(-14 * 24 * time.Hour).Unix(),
			Uptime:        "14d 3h",
			CPUPercent:    0.4,
			MemUsage:      48 * 1024 * 1024,
			MemLimit:      2 * 1024 * 1024 * 1024,
			MemPercent:    2.3,
			HumanMemUsage: "48.00 MB",
			HumanMemLimit: "2.00 GB",
		},
		{
			ID:            "d4e5f6a1b2c3",
			FullID:        "d4e5f6a1b2c3789012345678",
			Name:          "lizard",
			Image:         "lizard:latest",
			State:         "running",
			Status:        "Up 2 hours",
			Created:       time.Now().Add(-2 * time.Hour).Unix(),
			Uptime:        "2h 5m",
			CPUPercent:    0.1,
			MemUsage:      18 * 1024 * 1024,
			MemLimit:      1 * 1024 * 1024 * 1024,
			MemPercent:    1.8,
			HumanMemUsage: "18.00 MB",
			HumanMemLimit: "1.00 GB",
		},
	}
}
