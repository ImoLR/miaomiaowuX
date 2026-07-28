//go:build !windows

package handler

import "golang.org/x/sys/unix"

func localDiskUsage(path string) (diskStats, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return diskStats{}, err
	}
	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	used := uint64(0)
	if total >= free {
		used = total - free
	}
	return diskStats{used: used, total: total}, nil
}
