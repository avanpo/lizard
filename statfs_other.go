//go:build !linux

package main

import "io/fs"

type discoveredMount struct {
	DisplayPath string
	ScanPath    string
	Device      string
	FSType      string
}

// getDiskUsageStat returns dummy/stub data on non-Linux systems when syscall.Statfs is different.
func getDiskUsageStat(path string) (total uint64, free uint64, avail uint64, err error) {
	total = 500 * 1024 * 1024 * 1024
	free = 300 * 1024 * 1024 * 1024
	avail = free
	return total, free, avail, nil
}

func getDevID(path string) (uint64, bool) {
	return 0, false
}

func getFileInfoDev(info fs.FileInfo) (uint64, bool) {
	return 0, false
}

func discoverMounts(hostRoot string) []discoveredMount {
	return nil
}
