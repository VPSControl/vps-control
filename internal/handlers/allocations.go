package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

type AllocationHandlers struct {
	Store *store.Store
}

func (h *AllocationHandlers) List(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	if dep.Allocations == nil {
		dep.Allocations = []store.Allocation{}
	}
	middleware.JSON(w, http.StatusOK, dep.Allocations)
}

func (h *AllocationHandlers) Create(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserFromContext(r.Context())
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	var req struct {
		Port  string `json:"port"`
		Label string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}

	req.Port = strings.TrimSpace(req.Port)
	if _, err := strconv.Atoi(req.Port); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid port")
		return
	}
	if !portIsFree(req.Port) {
		middleware.JSONError(w, http.StatusConflict, "port "+req.Port+" is already in use")
		return
	}

	for _, a := range dep.Allocations {
		if a.Port == req.Port {
			middleware.JSONError(w, http.StatusConflict, "port already allocated to this app")
			return
		}
	}

	alloc := store.Allocation{
		ID:        randomID(),
		Port:      req.Port,
		Label:     req.Label,
		Primary:   len(dep.Allocations) == 0,
		CreatedAt: time.Now(),
	}
	dep.Allocations = append(dep.Allocations, alloc)
	if err := h.Store.UpdateDeployment(dep); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	internalPort := detectPort(dep.Path, dep.Stack).Port
	if internalPort == "" {
		internalPort = containerPortForStack(dep.Stack)
	}
	_, _ = runCommand(30*time.Second, "docker", "rm", "-f", dep.Container)
	runArgs := buildRunArgs(dep, internalPort)
	out, err := runCommand(30*time.Second, "docker", runArgs...)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "allocation saved but restart failed: "+out)
		return
	}

	_ = h.Store.AddActivity(store.ActivityEntry{
		ID:        randomID(),
		Timestamp: time.Now(),
		ActorID:   user.ID,
		ActorName: user.Username,
		Action:    "allocation.create",
		Target:    dep.Name,
		Details:   map[string]interface{}{"port": req.Port},
	})
	middleware.JSON(w, http.StatusCreated, alloc)
}

func (h *AllocationHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserFromContext(r.Context())
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	allocID := strings.TrimPrefix(r.URL.Path,
		fmt.Sprintf("/api/deployments/%s/allocations/", dep.ID))
	if allocID == "" {
		middleware.JSONError(w, http.StatusBadRequest, "missing allocation id")
		return
	}

	newAllocs := []store.Allocation{}
	found := false
	for _, a := range dep.Allocations {
		if a.ID == allocID {
			found = true
			continue
		}
		newAllocs = append(newAllocs, a)
	}
	if !found {
		middleware.JSONError(w, http.StatusNotFound, "allocation not found")
		return
	}
	dep.Allocations = newAllocs
	if len(dep.Allocations) > 0 {
		for i := range dep.Allocations {
			dep.Allocations[i].Primary = (i == 0)
		}
	}
	if err := h.Store.UpdateDeployment(dep); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	internalPort := detectPort(dep.Path, dep.Stack).Port
	if internalPort == "" {
		internalPort = containerPortForStack(dep.Stack)
	}
	_, _ = runCommand(30*time.Second, "docker", "rm", "-f", dep.Container)
	runArgs := buildRunArgs(dep, internalPort)
	_, _ = runCommand(30*time.Second, "docker", runArgs...)

	_ = h.Store.AddActivity(store.ActivityEntry{
		ID:        randomID(),
		Timestamp: time.Now(),
		ActorID:   user.ID,
		ActorName: user.Username,
		Action:    "allocation.delete",
		Target:    dep.Name,
	})
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// buildRunArgs construit les arguments `docker run` en tenant compte des
// limites de ressources et des allocations multiples.
func buildRunArgs(dep store.Deployment, internalPort string) []string {
	args := []string{"run", "-d", "--name", dep.Container, "--restart", "unless-stopped"}

	if dep.Limits.CPUQuota > 0 {
		args = append(args, "--cpus", fmt.Sprintf("%.2f", dep.Limits.CPUQuota/100.0))
	}
	if dep.Limits.MemoryMB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", dep.Limits.MemoryMB))
	}
	if dep.Limits.PidsLimit > 0 {
		args = append(args, "--pids-limit", fmt.Sprintf("%d", dep.Limits.PidsLimit))
	}

	allocs := dep.Allocations
	if len(allocs) == 0 {
		allocs = []store.Allocation{{Port: dep.Port, Primary: true}}
	}
	for _, a := range allocs {
		args = append(args, "-p", a.Port+":"+internalPort)
	}

	for _, v := range dep.EnvVars {
		args = append(args, "-e", v.Key+"="+v.Value)
	}

	// .env : uniquement s'il existe réellement
	envFile := dep.Path + "/.env"
	if _, err := os.Stat(envFile); err == nil {
		args = append(args, "--env-file", envFile)
	}

	imageTag := "vpscontrol-" + dep.Name
	args = append(args, imageTag)
	return args
}