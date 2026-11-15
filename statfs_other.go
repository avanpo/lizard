//go:build !linux

package main

// getDiskUsageStat returns dummy/stub data on non-Linux systems when syscall.Statfs is different.
func getDiskUsageStat(path string) (total uint64, free uint64, avail uint64, err error) {
	total = 500 * 1024 * 1024 * 1024
	free = 300 * 1024 * 1024 * 1024
	avail = free
	return total, free, avail, nil
}
