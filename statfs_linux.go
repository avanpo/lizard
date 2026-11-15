//go:build linux

package main

import (
	"bufio"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

type discoveredMount struct {
	DisplayPath string
	ScanPath    string
	Device      string
	FSType      string
}

// getDiskUsageStat returns total, free, and available bytes for a filesystem path on Linux.
func getDiskUsageStat(path string) (total uint64, free uint64, avail uint64, err error) {
	var stat syscall.Statfs_t
	err = syscall.Statfs(path, &stat)
	if err != nil {
		return 0, 0, 0, err
	}
	bsize := uint64(stat.Bsize)
	total = uint64(stat.Blocks) * bsize
	free = uint64(stat.Bfree) * bsize
	avail = uint64(stat.Bavail) * bsize
	return total, free, avail, nil
}

func getDevID(path string) (uint64, bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, false
	}
	return uint64(st.Dev), true
}

func getFileInfoDev(info fs.FileInfo) (uint64, bool) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), true
	}
	return 0, false
}

var ignoredFSTypes = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true,
	"tmpfs": true, "cgroup": true, "cgroup2": true, "pstore": true,
	"securityfs": true, "debugfs": true, "tracefs": true, "configfs": true,
	"fusectl": true, "mqueue": true, "hugetlbfs": true, "autofs": true,
	"binfmt_misc": true, "efivarfs": true, "nsfs": true, "overlay": true,
	"squashfs": true, "rpc_pipefs": true, "fuse.lxcfs": true, "bpf": true,
	"ramfs": true, "selinuxfs": true, "fuse.portal": true,
}

// discoverMounts reads the host mount table to find unique physical/network disks.
func discoverMounts(hostRoot string) []discoveredMount {
	mountsPath := "/proc/mounts"
	if hostRoot != "" && hostRoot != "/" {
		hostProcMounts := filepath.Join(hostRoot, "proc", "mounts")
		if _, err := os.Stat(hostProcMounts); err == nil {
			mountsPath = hostProcMounts
		} else if _, err := os.Stat("/proc/1/mounts"); err == nil {
			// With pid: "host", PID 1 is the host's init/systemd, so /proc/1/mounts has the host mount table
			mountsPath = "/proc/1/mounts"
		}
	}

	f, err := os.Open(mountsPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	type candidate struct {
		mount discoveredMount
		devID uint64
	}
	byDev := make(map[uint64]candidate)
	rootDevID, hasRootDev := getDevID(hostRoot)
	if hostRoot == "" {
		rootDevID, hasRootDev = getDevID("/")
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		dev := fields[0]
		mnt := unescapeMountPath(fields[1])
		fsType := fields[2]

		if ignoredFSTypes[fsType] {
			continue
		}
		// Skip container/system internal mount paths
		if strings.HasPrefix(mnt, "/proc") || strings.HasPrefix(mnt, "/sys") ||
			strings.HasPrefix(mnt, "/dev") || strings.HasPrefix(mnt, "/run") ||
			strings.Contains(mnt, "/docker/overlay2") || strings.Contains(mnt, "/docker/containers") ||
			strings.HasPrefix(mnt, "/snap/") || strings.HasPrefix(mnt, "/boot/efi") {
			continue
		}

		scanPath := mnt
		if hostRoot != "" && hostRoot != "/" {
			if mnt == "/" {
				scanPath = hostRoot
			} else {
				scanPath = filepath.Join(hostRoot, strings.TrimPrefix(mnt, "/"))
			}
		}

		fi, err := os.Stat(scanPath)
		if err != nil || !fi.IsDir() {
			continue
		}

		devID, ok := getDevID(scanPath)
		if !ok {
			continue
		}
		total, _, _, err := getDiskUsageStat(scanPath)
		if err != nil || total == 0 {
			continue
		}

		m := discoveredMount{
			DisplayPath: mnt,
			ScanPath:    scanPath,
			Device:      dev,
			FSType:      fsType,
		}

		// Deduplicate bind mounts of the same device, preferring shortest mount path (e.g. "/")
		if existing, exists := byDev[devID]; !exists || len(mnt) < len(existing.mount.DisplayPath) {
			byDev[devID] = candidate{mount: m, devID: devID}
		} else if hasRootDev && devID == rootDevID && mnt != "/" && existing.mount.Device != dev {
			log.Printf("Note: host mount %s (%s) is not visible inside %s (mount propagation is private). Use '- /:/host:ro,rslave' or mount %s directly.", mnt, dev, hostRoot, mnt)
		}
	}

	result := make([]discoveredMount, 0, len(byDev))
	for _, c := range byDev {
		result = append(result, c.mount)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].DisplayPath == "/" {
			return true
		}
		if result[j].DisplayPath == "/" {
			return false
		}
		return result[i].DisplayPath < result[j].DisplayPath
	})
	return result
}

func unescapeMountPath(s string) string {
	s = strings.ReplaceAll(s, `\040`, " ")
	s = strings.ReplaceAll(s, `\011`, "\t")
	s = strings.ReplaceAll(s, `\012`, "\n")
	s = strings.ReplaceAll(s, `\134`, `\`)
	return s
}
