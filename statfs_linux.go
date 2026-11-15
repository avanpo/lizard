//go:build linux

package main

import "syscall"

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
