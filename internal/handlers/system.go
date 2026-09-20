package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

// SystemHandlers groups the panel's self-administration endpoints: version
// info, manual update trigger, the GitHub webhook receiver for instant
// updates, and stats for the dashboard.
type SystemHandlers struct {
	Store         *store.Store
	SrcDir        string // folder the source code is cloned into (for git rev-parse / updates)
	RepoURL       string // upstream git repo, used to compare against the latest remote version
	WebhookSecret []byte // shared secret used to verify GitHub's webhook signature
}

func (h *SystemHandlers) Info(w http.ResponseWriter, r *http.Request) {
	commit, _ := runCommand(5*time.Second, "git", "-C", h.SrcDir, "rev-parse", "--short", "HEAD")
	middleware.JSON(w, http.StatusOK, map[string]string{
		"commit":  strings.TrimSpace(commit),
		"srcDir":  h.SrcDir,
		"repoUrl": h.RepoURL,
	})
}

// WebhookInfo returns what's needed to wire up the GitHub webhook by hand
// (Settings → Webhooks → Add webhook on the repo): the URL to call and the
// shared secret GitHub will sign its payloads with. Admin-only, since the
// secret is sensitive.
func (h *SystemHandlers) WebhookInfo(w http.ResponseWriter, r *http.Request) {
	middleware.JSON(w, http.StatusOK, map[string]string{
		"path":   "/api/webhook/update",
		"secret": hex.EncodeToString(h.WebhookSecret),
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

// Update triggers a manual update from the panel's System tab.
func (h *SystemHandlers) Update(w http.ResponseWriter, r *http.Request) {
	message, err := h.triggerUpdate("manual-update")
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]string{"message": message})
}

// Webhook is what GitHub calls on every push, so an update lands on the
// running service right away instead of waiting for the next daily check.
// Configure it on the repo (Settings → Webhooks) with:
//   Payload URL:  https://<your-panel-domain>/api/webhook/update
//   Content type: application/json
//   Secret:       shown in the panel's System tab (or GET /api/system/webhook, admin-only)
//   Events:       "Just the push event"
func (h *SystemHandlers) Webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "could not read the request body")
		return
	}
	if !validWebhookSignature(h.WebhookSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		middleware.JSONError(w, http.StatusUnauthorized, "invalid webhook signature")
		return
	}
	message, err := h.triggerUpdate("webhook")
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusAccepted, map[string]string{"message": message})
}

// validWebhookSignature checks GitHub's "X-Hub-Signature-256: sha256=<hex>" header.
func validWebhookSignature(secret, body []byte, header string) bool {
	if len(secret) == 0 || header == "" {
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	want := mac.Sum(nil)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// triggerUpdate schedules the update (git pull + rebuild + service restart)
// via systemd-run, detached from the current process: since this process is
// the one about to be restarted, it can't wait around for its own update to finish.
func (h *SystemHandlers) triggerUpdate(reason string) (string, error) {
	updateScript := h.SrcDir + "/scripts/update.sh"
	if _, err := os.Stat(updateScript); err != nil {
		return "", fmt.Errorf("update script not found (%s). Was the panel installed via git?", updateScript)
	}
	shellCmd := "sleep 2 && bash " + updateScript + " >> /var/log/vpscontrol-update.log 2>&1"
	unit := "vpscontrol-" + reason + "-" + strconv.FormatInt(time.Now().Unix(), 10)
	out, err := runCommand(10*time.Second, "systemd-run", "--no-block", "--unit="+unit,
		"/bin/bash", "-c", shellCmd)
	if err != nil {
		return "", fmt.Errorf("could not start the update: %s", out)
	}
	return "Update started. The panel will restart in a few seconds.", nil
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
