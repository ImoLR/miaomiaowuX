package handler

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

type DashboardSystemMetrics struct {
	CPUPct    float64 `json:"cpu_pct"`
	CPUCores  int     `json:"cpu_cores,omitempty"`
	MemUsed   uint64  `json:"mem_used"`
	MemTotal  uint64  `json:"mem_total"`
	SwapUsed  uint64  `json:"swap_used"`
	SwapTotal uint64  `json:"swap_total"`
	DiskUsed  uint64  `json:"disk_used"`
	DiskTotal uint64  `json:"disk_total"`
}

type DashboardSystemHandler struct {
	mu       sync.Mutex
	lastCPU  cpuTimes
	hasCPU   bool
	readStat func() (cpuTimes, error)
	readMem  func() (memoryStats, error)
	readDisk func(string) (diskStats, error)
}

type cpuTimes struct {
	user    uint64
	nice    uint64
	system  uint64
	idle    uint64
	iowait  uint64
	irq     uint64
	softirq uint64
	steal   uint64
}

type memoryStats struct {
	memTotal     uint64
	memAvailable uint64
	swapTotal    uint64
	swapFree     uint64
	hasMemTotal  bool
	hasMemAvail  bool
	hasSwapTotal bool
	hasSwapFree  bool
}

type diskStats struct {
	used  uint64
	total uint64
}

func NewDashboardSystemHandler() *DashboardSystemHandler {
	h := &DashboardSystemHandler{
		readStat: readProcStatCPU,
		readMem:  readProcMeminfo,
		readDisk: localDiskUsage,
	}
	if first, err := h.readStat(); err == nil {
		h.lastCPU = first
		h.hasCPU = true
	}
	return h
}

func (h *DashboardSystemHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "message": "method not allowed"})
		return
	}

	metrics, err := h.Snapshot()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "message": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(metrics)
}

func (h *DashboardSystemHandler) Snapshot() (DashboardSystemMetrics, error) {
	mem, err := h.readMem()
	if err != nil {
		return DashboardSystemMetrics{}, err
	}
	disk, err := h.readDisk("/")
	if err != nil {
		return DashboardSystemMetrics{}, err
	}

	memUsed := uint64(0)
	if mem.hasMemTotal && mem.hasMemAvail && mem.memTotal >= mem.memAvailable {
		memUsed = mem.memTotal - mem.memAvailable
	}
	swapUsed := uint64(0)
	if mem.hasSwapTotal && mem.hasSwapFree && mem.swapTotal >= mem.swapFree {
		swapUsed = mem.swapTotal - mem.swapFree
	}

	return DashboardSystemMetrics{
		CPUPct:    h.cpuPercent(),
		CPUCores:  runtime.NumCPU(),
		MemUsed:   memUsed,
		MemTotal:  mem.memTotal,
		SwapUsed:  swapUsed,
		SwapTotal: mem.swapTotal,
		DiskUsed:  disk.used,
		DiskTotal: disk.total,
	}, nil
}

func (h *DashboardSystemHandler) cpuPercent() float64 {
	cur, err := h.readStat()
	if err != nil {
		return 0
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.hasCPU {
		h.lastCPU = cur
		h.hasCPU = true
		return 0
	}

	prev := h.lastCPU
	h.lastCPU = cur
	idleDelta := diffCPU(cur.idle+cur.iowait, prev.idle+prev.iowait)
	totalDelta := diffCPU(cur.total(), prev.total())
	if totalDelta == 0 || idleDelta > totalDelta {
		return 0
	}
	used := float64(totalDelta-idleDelta) / float64(totalDelta) * 100
	return clampMetricPercent(used)
}

func (t cpuTimes) total() uint64 {
	return t.user + t.nice + t.system + t.idle + t.iowait + t.irq + t.softirq + t.steal
}

func diffCPU(cur, prev uint64) uint64 {
	if cur < prev {
		return 0
	}
	return cur - prev
}

func readProcStatCPU() (cpuTimes, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuTimes{}, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 || fields[0] != "cpu" {
			continue
		}
		values := make([]uint64, 8)
		for i := range values {
			n, err := strconv.ParseUint(fields[i+1], 10, 64)
			if err != nil {
				return cpuTimes{}, err
			}
			values[i] = n
		}
		return cpuTimes{
			user: values[0], nice: values[1], system: values[2], idle: values[3],
			iowait: values[4], irq: values[5], softirq: values[6], steal: values[7],
		}, nil
	}
	return cpuTimes{}, errors.New("cpu line not found")
}

func readProcMeminfo() (memoryStats, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return memoryStats{}, err
	}
	return parseMeminfo(string(data)), nil
}

func parseMeminfo(data string) memoryStats {
	stats := memoryStats{}
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		valueKiB, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		valueBytes := valueKiB * 1024
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemTotal":
			stats.memTotal = valueBytes
			stats.hasMemTotal = true
		case "MemAvailable":
			stats.memAvailable = valueBytes
			stats.hasMemAvail = true
		case "MemFree":
			if !stats.hasMemAvail {
				stats.memAvailable = valueBytes
			}
		case "SwapTotal":
			stats.swapTotal = valueBytes
			stats.hasSwapTotal = true
		case "SwapFree":
			stats.swapFree = valueBytes
			stats.hasSwapFree = true
		}
	}
	return stats
}

func clampMetricPercent(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
