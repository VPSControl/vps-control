package handlers

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// diskUsage returns (used, total) in bytes for the given mount point.
func diskUsage(path string) (used, total uint64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0
	}
	total = stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	if free > total {
		return 0, total
	}
	return total - free, total
}

// memoryUsage returns (used, total) in bytes, read from /proc/meminfo.
// Deliberately simple (no external dependency): good enough for the
// dashboard overview.
func memoryUsage() (used, total uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()

	values := map[string]uint64{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		key := strings.TrimSuffix(parts[0], ":")
		n, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			continue
		}
		values[key] = n * 1024 // /proc/meminfo is in kB
	}
	total = values["MemTotal"]
	available := values["MemAvailable"]
	if available > total {
		return 0, total
	}
	return total - available, total
}
