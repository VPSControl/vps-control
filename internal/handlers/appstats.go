package handlers

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

	"vpscontrol/internal/middleware"
)

type AppStatsHandlers struct{}

type containerStat struct {
	Name       string  `json:"name"`
	CPUPercent float64 `json:"cpuPercent"`
	MemoryUsed int64   `json:"memoryUsed"`
	MemoryMax  int64   `json:"memoryMax"`
	NetRx      int64   `json:"netRx"`
	NetTx      int64   `json:"netTx"`
	BlockRead  int64   `json:"blockRead"`
	BlockWrite int64   `json:"blockWrite"`
	PIDs       int64   `json:"pids"`
}

func (h *AppStatsHandlers) Live(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	stats := collectContainerStat(dep.Container)
	middleware.JSON(w, http.StatusOK, stats)
}

func collectContainerStat(container string) containerStat {
	out := containerStat{Name: container}
	cmd := exec.Command("docker", "stats", "--no-stream", "--format", "{{json .}}", container)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return out
	}
	if err := cmd.Start(); err != nil {
		return out
	}
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		var raw map[string]string
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		out.CPUPercent = parsePercent(raw["CPUPerc"])
		out.MemoryUsed, out.MemoryMax = parseMemoryPair(raw["MemUsage"])
		out.NetRx, out.NetTx = parseIOPair(raw["NetIO"])
		out.BlockRead, out.BlockWrite = parseIOPair(raw["BlockIO"])
		out.PIDs = parseIntSafe(raw["PIDs"])
	}
	_ = cmd.Wait()
	return out
}

func parsePercent(s string) float64 {
	s = strings.TrimSuffix(strings.TrimSpace(s), "%")
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func parseMemoryPair(s string) (int64, int64) {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return 0, 0
	}
	return parseBytes(parts[0]), parseBytes(parts[1])
}

func parseIOPair(s string) (int64, int64) {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return 0, 0
	}
	return parseBytes(parts[0]), parseBytes(parts[1])
}

func parseBytes(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "--" {
		return 0
	}
	units := []struct {
		suffix string
		mult   float64
	}{
		{"TiB", 1024 * 1024 * 1024 * 1024},
		{"GiB", 1024 * 1024 * 1024},
		{"MiB", 1024 * 1024},
		{"KiB", 1024},
		{"TB", 1e12},
		{"GB", 1e9},
		{"MB", 1e6},
		{"kB", 1e3},
		{"KB", 1e3},
		{"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, u.suffix)), 64)
			if err != nil {
				return 0
			}
			return int64(n * u.mult)
		}
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func parseIntSafe(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}