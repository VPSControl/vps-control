package handlers

import (
	"net/http"
	"os"
	"strings"
	"time"

	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

// =====================================================================
// Power actions scopées sur une app.
//
// Le code est dans l'image (immuable après build). Un simple Restart ne
// reflète PAS les modifications de code — il faut un Reinstall.
//
//   - Start     → démarre le conteneur. S'il n'existe pas, build + run.
//   - Restart   → INTELLIGENT : rebuild automatique si le stack a un
//                 step de build (React, Next, Vite, Astro, SvelteKit,
//                 Nuxt) OU si Node a un script build. Sinon, restart.
//   - Stop      → docker stop.
//   - Kill      → docker kill.
//   - Reinstall → rebuild forcé + recréation. À utiliser après toute
//                 modification de code.
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

func (h *DeployHandlers) PowerReinstall(w http.ResponseWriter, r *http.Request) {
	h.powerAction(w, r, "reinstall")
}

// =====================================================================
// powerAction — dispatcher
// =====================================================================

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

	switch action {
	case "start":
		h.doStart(w, dep)
	case "stop":
		h.doStop(w, dep)
	case "kill":
		h.doKill(w, dep)
	case "restart":
		if stackNeedsRebuildOnRestart(dep) {
			h.doReinstall(w, dep, "restart (auto-rebuild)")
		} else {
			h.doSimpleRestart(w, dep)
		}
	case "reinstall":
		h.doReinstall(w, dep, "reinstall")
	default:
		middleware.JSONError(w, http.StatusBadRequest, "unknown power action")
	}
}

func (h *DeployHandlers) doStart(w http.ResponseWriter, dep store.Deployment) {
	statusOut, _ := runCommand(5*time.Second, "docker", "inspect",
		"--format", "{{.State.Status}}", dep.Container)
	status := strings.TrimSpace(statusOut)

	if status == "" {
		h.doReinstall(w, dep, "start (initial build)")
		return
	}

	if status == "running" {
		middleware.JSON(w, http.StatusOK, map[string]interface{}{
			"ok":      true,
			"action":  "start",
			"message": "Container is already running",
		})
		return
	}

	out, err := runCommand(30*time.Second, "docker", "start", dep.Container)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "docker start failed: "+out)
		return
	}

	dep.Status = "running"
	dep.LastError = ""
	_ = h.Store.UpdateDeployment(dep)

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"ok":     true,
		"action": "start",
		"output": out,
	})
}

func (h *DeployHandlers) doStop(w http.ResponseWriter, dep store.Deployment) {
	out, err := runCommand(30*time.Second, "docker", "stop", dep.Container)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "docker stop failed: "+out)
		return
	}

	dep.Status = "stopped"
	_ = h.Store.UpdateDeployment(dep)

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"ok":     true,
		"action": "stop",
		"output": out,
	})
}

func (h *DeployHandlers) doKill(w http.ResponseWriter, dep store.Deployment) {
	out, err := runCommand(30*time.Second, "docker", "kill", dep.Container)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "docker kill failed: "+out)
		return
	}

	dep.Status = "stopped"
	_ = h.Store.UpdateDeployment(dep)

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"ok":     true,
		"action": "kill",
		"output": out,
	})
}

func (h *DeployHandlers) doSimpleRestart(w http.ResponseWriter, dep store.Deployment) {
	out, err := runCommand(45*time.Second, "docker", "restart", dep.Container)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "docker restart failed: "+out)
		return
	}

	dep.Status = "running"
	dep.LastError = ""
	_ = h.Store.UpdateDeployment(dep)

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"ok":      true,
		"action":  "restart",
		"mode":    "fast",
		"message": "Container restarted. The code was NOT rebuilt.",
		"output":  out,
	})
}

func (h *DeployHandlers) doReinstall(w http.ResponseWriter, dep store.Deployment, trigger string) {
	if dep.AppSubdir == "" {
		dep.AppSubdir = findAppRoot(dep.Path)
	}
	effDir := appRoot(dep)
	if _, err := os.Stat(effDir); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "app root not found: "+effDir)
		return
	}

	stack := detectStack(effDir)
	if stack != "" && stack != "auto" {
		dep.Stack = stack
	}

	internalPort := detectPort(effDir, dep.Stack).Port
	if internalPort == "" {
		internalPort = containerPortForStack(dep.Stack)
	}

	opts := buildOptions{
		Stack:         dep.Stack,
		NodeVersion:   normalizeNodeVersion(dep.NodeVersion),
		PythonVersion: normalizePythonVersion(dep.PythonVersion),
		PHPVersion:    normalizePHPVersion(dep.PHPVersion),
		HostPort:      dep.Port,
		InternalPort:  internalPort,
	}
	if err := writeDockerfile(effDir, opts); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "failed to write Dockerfile: "+err.Error())
		return
	}

	dep.Status = "deploying"
	dep.LastError = ""
	_ = h.Store.UpdateDeployment(dep)

	imageTag := "vpscontrol-" + dep.Name
	out, err := runCommand(5*time.Minute, "docker", "build", "--no-cache", "-t", imageTag, effDir)
	if err != nil {
		dep.Status = "error"
		dep.LastError = truncateError("docker build failed:\n" + out)
		dep.LastErrorAt = time.Now()
		_ = h.Store.UpdateDeployment(dep)
		middleware.JSONError(w, http.StatusInternalServerError, dep.LastError)
		return
	}

	_, _ = runCommand(30*time.Second, "docker", "rm", "-f", dep.Container)

	runArgs := buildRunArgs(dep, internalPort)
	out, err = runCommand(30*time.Second, "docker", runArgs...)
	if err != nil {
		dep.Status = "error"
		dep.LastError = truncateError("failed to start container:\n" + out)
		dep.LastErrorAt = time.Now()
		_ = h.Store.UpdateDeployment(dep)
		middleware.JSONError(w, http.StatusInternalServerError, dep.LastError)
		return
	}

	dep.Status = "running"
	_ = h.Store.UpdateDeployment(dep)

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"ok":      true,
		"action":  "reinstall",
		"trigger": trigger,
		"stack":   dep.Stack,
		"message": "Container rebuilt and restarted. Your changes are now live.",
		"output":  out,
	})
}

func stackNeedsRebuildOnRestart(dep store.Deployment) bool {
	switch dep.Stack {
	case "react", "next", "astro", "sveltekit", "nuxt":
		return true
	case "node":
		return nodeHasBuildScript(appRoot(dep))
	default:
		return false
	}
}