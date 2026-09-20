package handlers

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

// SystemHandlers groups the panel's self-administration endpoints: version
// info, update check/trigger, and stats for the dashboard.
type SystemHandlers struct {
	Store   *store.Store
	SrcDir  string // folder the source code is cloned into (for git rev-parse / updates)
	RepoURL string // upstream git repo, used to compare against the latest remote version
}

func (h *SystemHandlers) Info(w http.ResponseWriter, r *http.Request) {
	commit, _ := runCommand(5*time.Second, "git", "-C", h.SrcDir, "rev-parse", "--short", "HEAD")
	middleware.JSON(w, http.StatusOK, map[string]string{
		"commit":  strings.TrimSpace(commit),
		"srcDir":  h.SrcDir,
		"repoUrl": h.RepoURL,
	})
}

func (h *SystemHandlers) CheckUpdate(w http.ResponseWriter, r *http.Request) {
	localOut, err := runCommand(10*time.Second, "git", "-C", h.SrcDir, "rev-parse", "HEAD")
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "could not read the local version (was the panel installed via git?): "+localOut)
		return
	}
	local := strings.TrimSpace(localOut)

	remoteOut, err := runCommand(20*time.Second, "git", "ls-remote", h.RepoURL, "HEAD")
	if err != nil {
		middleware.JSONError(w, http.StatusBadGateway, "could not reach the remote repository: "+remoteOut)
		return
	}
	fields := strings.Fields(remoteOut)
	if len(fields) == 0 {
		middleware.JSONError(w, http.StatusBadGateway, "unexpected response from the remote repository")
		return
	}
	remote := fields[0]

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"localCommit":     local[:min(len(local), 10)],
		"remoteCommit":    remote[:min(len(remote), 10)],
		"updateAvailable": local != remote,
	})
}

// Update schedules the update (git pull + rebuild + service restart) via
// systemd-run, detached from the current process: since this process is the
// one about to be restarted, it can't wait around for its own update to finish.
func (h *SystemHandlers) Update(w http.ResponseWriter, r *http.Request) {
	updateScript := h.SrcDir + "/scripts/update.sh"
	if _, err := os.Stat(updateScript); err != nil {
		middleware.JSONError(w, http.StatusNotFound, "update script not found ("+updateScript+"). Was the panel installed via git?")
		return
	}
	shellCmd := "sleep 2 && bash " + updateScript + " >> /var/log/vpscontrol-update.log 2>&1"
	out, err := runCommand(10*time.Second, "systemd-run", "--no-block",
		"--unit=vpscontrol-manual-update-"+strconv.FormatInt(time.Now().Unix(), 10),
		"/bin/bash", "-c", shellCmd)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "could not start the update: "+out)
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]string{
		"message": "Update started. The panel will restart in a few seconds.",
	})
}

// Stats feeds the dashboard cards.
func (h *SystemHandlers) Stats(w http.ResponseWriter, r *http.Request) {
	servicesTotal, servicesRunning := dockerCounts()
	diskUsed, diskTotal := diskUsage("/")
	memUsed, memTotal := memoryUsage()

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"servicesRunning": servicesRunning,
		"servicesTotal":   servicesTotal,
		"deployments":     len(h.Store.ListDeployments()),
		"diskUsedBytes":   diskUsed,
		"diskTotalBytes":  diskTotal,
		"memUsedBytes":    memUsed,
		"memTotalBytes":   memTotal,
	})
}

func dockerCounts() (total, running int) {
	out, err := runCommand(10*time.Second, "docker", "ps", "-a", "--format", "{{.Status}}")
	if err != nil {
		return 0, 0
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		total++
		if strings.HasPrefix(strings.ToLower(line), "up") {
			running++
		}
	}
	return total, running
}
