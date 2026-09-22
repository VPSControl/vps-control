package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

type EnvHandlers struct {
	Store *store.Store
}

func (h *EnvHandlers) List(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	if dep.EnvVars == nil {
		dep.EnvVars = []store.EnvVar{}
	}
	middleware.JSON(w, http.StatusOK, dep.EnvVars)
}

func (h *EnvHandlers) Set(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserFromContext(r.Context())
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	var req struct {
		Vars []store.EnvVar `json:"vars"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}

	cleaned := []store.EnvVar{}
	seen := map[string]bool{}
	for _, v := range req.Vars {
		k := strings.TrimSpace(v.Key)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		cleaned = append(cleaned, store.EnvVar{Key: k, Value: v.Value})
	}

	dep.EnvVars = cleaned
	if err := h.Store.UpdateDeployment(dep); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Écrire aussi dans /opt/vpscontrol/apps/{name}/.env
	if out, err := WriteEnvFile(dep.Path, cleaned); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "saved to store but .env write failed: "+out)
		return
	}

	_ = h.Store.AddActivity(store.ActivityEntry{
		ID:        randomID(),
		Timestamp: time.Now(),
		ActorID:   user.ID,
		ActorName: user.Username,
		Action:    "env.update",
		Target:    dep.Name,
	})
	middleware.JSON(w, http.StatusOK, dep.EnvVars)
}