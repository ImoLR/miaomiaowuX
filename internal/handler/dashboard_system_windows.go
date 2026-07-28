//go:build windows

package handler

func localDiskUsage(_ string) (diskStats, error) {
	return diskStats{}, nil
}
