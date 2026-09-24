package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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

	internalPort := detectPort(appRoot(dep), dep.Stack).Port
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

	internalPort := detectPort(appRoot(dep), dep.Stack).Port
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

// =====================================================================
// buildRunArgs — construit les arguments `docker run`
// =====================================================================
//
// IMPORTANT : on ne monte JAMAIS le dossier entier de l'app dans /app.
// Cela écraserait le code de l'image (package.json, node_modules, etc.)
// et provoquerait l'erreur ENOENT /app/package.json.
//
// À la place, on monte UNIQUEMENT les sous-dossiers de données S'ILS
// EXISTENT sur l'hôte :
//   - uploads/
//   - storage/
//   - data/
//   - logs/
//   - public/uploads/
//   - public/storage/
//
// Ces dossiers sont ceux que les apps écrivent au runtime (fichiers
// uploadés par les users, cache, logs applicatifs). Ils persistent entre
// les redémarrages et sont visibles dans l'onglet Files.
func buildRunArgs(dep store.Deployment, internalPort string) []string {
	args := []string{"run", "-d", "--name", dep.Container, "--restart", "unless-stopped"}

	// Limites de ressources
	if dep.Limits.CPUQuota > 0 {
		args = append(args, "--cpus", fmt.Sprintf("%.2f", dep.Limits.CPUQuota/100.0))
	}
	if dep.Limits.MemoryMB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", dep.Limits.MemoryMB))
	}
	if dep.Limits.PidsLimit > 0 {
		args = append(args, "--pids-limit", fmt.Sprintf("%d", dep.Limits.PidsLimit))
	}

	// Ports exposés
	allocs := dep.Allocations
	if len(allocs) == 0 {
		allocs = []store.Allocation{{Port: dep.Port, Primary: true}}
	}
	for _, a := range allocs {
		args = append(args, "-p", a.Port+":"+internalPort)
	}

	// Variables d'environnement individuelles
	for _, v := range dep.EnvVars {
		args = append(args, "-e", v.Key+"="+v.Value)
	}

	// ---- Dossiers de données (volume ciblé, jamais tout /app) ----
	appDir := appRoot(dep)
	workdir := containerWorkdir(dep)
	dataFolders := []string{
		"uploads",
		"storage",
		"data",
		"logs",
		"public/uploads",
		"public/storage",
	}
	for _, sub := range dataFolders {
		hostPath := filepath.Join(appDir, sub)
		if _, err := os.Stat(hostPath); err == nil {
			containerPath := filepath.Join(workdir, sub)
			// Si le sous-dossier parent n'existe pas dans le conteneur,
			// Docker le crée automatiquement (comportement par défaut).
			args = append(args, "-v", hostPath+":"+containerPath)
		}
	}

	// .env : uniquement s'il existe réellement (dans le dossier de l'app)
	envFile := appDir + "/.env"
	if _, err := os.Stat(envFile); err == nil {
		args = append(args, "--env-file", envFile)
	}

	imageTag := "vpscontrol-" + dep.Name
	args = append(args, imageTag)
	return args
}

// containerWorkdir retourne le chemin de travail dans le conteneur
// selon la stack de l'app.
//
// Pour Node/Python/Go : /app (standard)
// Pour Laravel : /var/www/html (là où Apache lit)
// Pour Static : /usr/share/nginx/html (là où Nginx lit)
func containerWorkdir(dep store.Deployment) string {
	switch dep.Stack {
	case "laravel":
		return "/var/www/html"
	case "static":
		return "/usr/share/nginx/html"
	default:
		return "/app"
	}
}