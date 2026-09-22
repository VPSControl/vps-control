package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

// WebhookHandlers : reçoit les événements GitHub pour redéployer le panel.
type WebhookHandlers struct {
	Store  *store.Store
	SrcDir string
}

// payload minimal de l'événement "push" GitHub.
type githubPushPayload struct {
	Ref        string `json:"ref"`
	Repository struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
		SSHURL   string `json:"ssh_url"`
	} `json:"repository"`
	Pusher struct {
		Name string `json:"name"`
	} `json:"pusher"`
}

// GitHub : endpoint POST /api/webhook/github
//
// Vérifie la signature HMAC-SHA256 fournie dans l'en-tête
// X-Hub-Signature-256 contre le secret stocké, puis déclenche update.sh
// de façon détachée (via systemd-run) pour que le panel puisse redémarrer
// sans couper la réponse HTTP en cours.
//
// IMPORTANT : cet endpoint est PUBLIC (pas d'auth cookie) car GitHub ne
// peut pas s'authentifier autrement que par la signature HMAC.
func (h *WebhookHandlers) GitHub(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		middleware.JSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	secret := h.Store.GetWebhookSecret()
	if secret == "" {
		middleware.JSONError(w, http.StatusServiceUnavailable, "webhook secret not configured")
		return
	}

	// Lire le body AVANT de vérifier la signature (max 1 Mo).
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "could not read body")
		return
	}

	// Vérifier la signature X-Hub-Signature-256 (format "sha256=hex").
	sigHeader := r.Header.Get("X-Hub-Signature-256")
	if !verifyGitHubSignature(secret, body, sigHeader) {
		middleware.JSONError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	// Événement GitHub : on ne traite que "push".
	event := r.Header.Get("X-GitHub-Event")
	if event == "" || event == "ping" {
		middleware.JSON(w, http.StatusOK, map[string]string{"status": "pong"})
		return
	}
	if event != "push" {
		middleware.JSON(w, http.StatusOK, map[string]string{"status": "ignored", "event": event})
		return
	}

	var payload githubPushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	// Optionnel : ne redéployer que si le push concerne la branche main/master.
	// Sinon on redéploie quand même (utile pour les tags, branches multiples).
	branch := strings.TrimPrefix(payload.Ref, "refs/heads/")
	_ = branch

	// Déclencher update.sh détaché du process courant.
	updateScript := h.SrcDir + "/scripts/update.sh"
	if _, err := os.Stat(updateScript); err != nil {
		middleware.JSONError(w, http.StatusNotFound, "update script not found: "+updateScript)
		return
	}
	unitName := "vpscontrol-webhook-" + strconv.FormatInt(time.Now().Unix(), 10)
	shellCmd := "sleep 2 && bash " + updateScript + " >> /var/log/vpscontrol-update.log 2>&1"
	if out, err := runCommand(10*time.Second, "systemd-run", "--no-block",
		"--unit="+unitName, "/bin/bash", "-c", shellCmd); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "could not start the update: "+out)
		return
	}

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"status":   "update scheduled",
		"branch":   branch,
		"pusher":   payload.Pusher.Name,
		"repo":     payload.Repository.FullName,
	})
}

// verifyGitHubSignature compare la signature fournie par GitHub avec
// celle calculée localement, en temps constant.
func verifyGitHubSignature(secret string, body []byte, header string) bool {
	if header == "" {
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	expectedHex := strings.TrimPrefix(header, prefix)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	actualHex := hex.EncodeToString(mac.Sum(nil))

	// Comparaison temps constant sur les chaînes hexadécimales.
	return hmac.Equal([]byte(expectedHex), []byte(actualHex))
}