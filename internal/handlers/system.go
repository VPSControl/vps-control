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

// SystemHandlers regroupe les endpoints d'auto-administration du panel :
// informations de version, vérification/déclenchement de mise à jour, et
// statistiques pour le tableau de bord.
type SystemHandlers struct {
	Store   *store.Store
	SrcDir  string // dossier où est cloné le code source (pour git rev-parse / mise à jour)
	RepoURL string // dépôt git d'origine, utilisé pour comparer avec la dernière version distante
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
		middleware.JSONError(w, http.StatusInternalServerError, "impossible de lire la version locale (le panel a-t-il été installé via git ?): "+localOut)
		return
	}
	local := strings.TrimSpace(localOut)

	remoteOut, err := runCommand(20*time.Second, "git", "ls-remote", h.RepoURL, "HEAD")
	if err != nil {
		middleware.JSONError(w, http.StatusBadGateway, "impossible de contacter le dépôt distant: "+remoteOut)
		return
	}
	fields := strings.Fields(remoteOut)
	if len(fields) == 0 {
		middleware.JSONError(w, http.StatusBadGateway, "réponse inattendue du dépôt distant")
		return
	}
	remote := fields[0]

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"localCommit":     local[:min(len(local), 10)],
		"remoteCommit":    remote[:min(len(remote), 10)],
		"updateAvailable": local != remote,
	})
}

// Update planifie la mise à jour (git pull + rebuild + redémarrage du service)
// via systemd-run, détachée du processus courant : comme ce processus est celui
// qui va être redémarré, il ne peut pas attendre la fin de sa propre mise à jour.
func (h *SystemHandlers) Update(w http.ResponseWriter, r *http.Request) {
	updateScript := h.SrcDir + "/scripts/update.sh"
	if _, err := os.Stat(updateScript); err != nil {
		middleware.JSONError(w, http.StatusNotFound, "script de mise à jour introuvable ("+updateScript+"). Le panel a-t-il été installé via git ?")
		return
	}
	shellCmd := "sleep 2 && bash " + updateScript + " >> /var/log/vpscontrol-update.log 2>&1"
	out, err := runCommand(10*time.Second, "systemd-run", "--no-block",
		"--unit=vpscontrol-manual-update-"+strconv.FormatInt(time.Now().Unix(), 10),
		"/bin/bash", "-c", shellCmd)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "impossible de lancer la mise à jour: "+out)
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]string{
		"message": "Mise à jour lancée. Le panel va redémarrer dans quelques secondes.",
	})
}

// Stats alimente les cartes du tableau de bord.
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
