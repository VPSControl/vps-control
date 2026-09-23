package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

// SystemHandlers groups the panel's self-administration endpoints.
type SystemHandlers struct {
	Store   *store.Store
	SrcDir  string
	RepoURL string
}

// generateWebhookSecret : 32 bytes aléatoires en hex (64 chars).
func generateWebhookSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Info renvoie la version locale, l'URL du repo et les infos webhook.
func (h *SystemHandlers) Info(w http.ResponseWriter, r *http.Request) {
	commit, _ := runCommand(5*time.Second, "git", "-C", h.SrcDir, "rev-parse", "--short", "HEAD")

	secret, err := h.Store.EnsureWebhookSecret(generateWebhookSecret)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	webhookURL := scheme + "://" + host + "/api/webhook/github"

	middleware.JSON(w, http.StatusOK, map[string]string{
		"commit":        strings.TrimSpace(commit),
		"srcDir":        h.SrcDir,
		"repoUrl":       h.RepoURL,
		"webhookUrl":    webhookURL,
		"webhookSecret": secret,
	})
}

// RegenerateWebhookSecret : permet de changer le secret depuis le panel.
func (h *SystemHandlers) RegenerateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	secret, err := generateWebhookSecret()
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.Store.SetWebhookSecret(secret); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]string{"webhookSecret": secret})
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
// systemd-run, detached from the current process.
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
//
// Isolation stricte : les compteurs "services" sont calculés à partir des
// déploiements visibles par l'utilisateur courant (owner OU subuser). Un
// admin ne voit donc QUE ses propres services + ceux où il est invité.
// Le disque et la mémoire restent ceux du VPS entier (ressource physique
// partagée — ce serait mensonger de la découper par user).
func (h *SystemHandlers) Stats(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	visibleDeps := h.Store.ListDeploymentsForUser(user.ID, user.Role)

	servicesRunning, servicesTotal := 0, 0
	for _, d := range visibleDeps {
		if d.Status == "draft" {
			continue
		}
		servicesTotal++
		statusOut, _ := runCommand(5*time.Second, "docker", "inspect",
			"--format", "{{.State.Status}}", d.Container)
		if strings.TrimSpace(statusOut) == "running" {
			servicesRunning++
		}
	}

	diskUsed, diskTotal := diskUsage("/")
	memUsed, memTotal := memoryUsage()

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"servicesRunning": servicesRunning,
		"servicesTotal":   servicesTotal,
		"deployments":     len(visibleDeps),
		"diskUsedBytes":   diskUsed,
		"diskTotalBytes":  diskTotal,
		"memUsedBytes":    memUsed,
		"memTotalBytes":   memTotal,
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}