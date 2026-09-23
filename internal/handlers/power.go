package handlers

import (
	"net/http"
	"time"

	"vpscontrol/internal/middleware"
)

// =====================================================================
// Power actions scopées sur une app.
//
// Ces handlers sont utilisés par /api/deployments/{id}/power/{action} :
// l'utilisateur agit sur SON conteneur (récupéré depuis le contexte
// après vérification d'accès via RequireDeploymentAccess), jamais sur un
// conteneur arbitraire — contrairement à ServiceHandlers qui accepte
// n'importe quel nom (réservé aux admins sur la page /services.html).
// =====================================================================

func (h *DeployHandlers) PowerStart(w http.ResponseWriter, r *http.Request) {
	h.powerAction(w, r, "start")
}

func (h *DeployHandlers) PowerStop(w http.ResponseWriter, r *http.Request) {
	h.powerAction(w, r, "stop")
}

func (h *DeployHandlers) PowerRestart(w http.ResponseWriter, r *http.Request) {
	h.powerAction(w, r, "restart")
}

func (h *DeployHandlers) PowerKill(w http.ResponseWriter, r *http.Request) {
	h.powerAction(w, r, "kill")
}

func (h *DeployHandlers) powerAction(w http.ResponseWriter, r *http.Request, action string) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	if dep.Container == "" {
		middleware.JSONError(w, http.StatusBadRequest, "this deployment has no container")
		return
	}

	var dockerVerb string
	switch action {
	case "start":
		dockerVerb = "start"
	case "stop":
		dockerVerb = "stop"
	case "restart":
		dockerVerb = "restart"
	case "kill":
		dockerVerb = "kill"
	default:
		middleware.JSONError(w, http.StatusBadRequest, "unknown power action")
		return
	}

	// Timeout généreux : "stop" peut prendre 10-20s si l'app ignore SIGTERM.
	out, err := runCommand(45*time.Second, "docker", dockerVerb, dep.Container)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, out)
		return
	}

	// Mettre à jour le statut en base pour que le dashboard reflète l'état.
	switch action {
	case "start", "restart":
		dep.Status = "running"
		dep.LastError = ""
		_ = h.Store.UpdateDeployment(dep)
	case "stop", "kill":
		dep.Status = "stopped"
		_ = h.Store.UpdateDeployment(dep)
	}

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"ok":     true,
		"action": action,
		"output": out,
	})
}