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

// buildRunArgs construit les arguments `docker run` en tenant compte des
// limites de ressources, des allocations multiples, et du mode volume.
//
// Le mode volume (Wings-like) monte le dossier de l'app sur l'hôte dans
// le conteneur, ce qui permet à l'utilisateur de modifier un fichier dans
// le panel puis de cliquer sur Restart pour appliquer le changement —
// sans rebuild.
//
// Le montage est conditionnel :
//   - Les stacks avec build step (React, Next, Vite, Astro, SvelteKit, Nuxt)
//     NE sont PAS montés en volume : leur code doit être re-buildé.
//   - Les stacks sans build (Node générique sans scripts.build, Python, Go,
//     Laravel, Static) SONT montés en volume : code modifiable à chaud.
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

	// Mode volume : monter le dossier de l'app dans le conteneur.
	// Conditionnel selon le stack (voir needsVolumeMount).
	if needsVolumeMount(dep) {
		workdir := containerWorkdir(dep)
		args = append(args, "-v", dep.Path+":"+workdir)
	}

	// .env : uniquement s'il existe réellement (dans le dossier de l'app)
	envFile := appRoot(dep) + "/.env"
	if _, err := os.Stat(envFile); err == nil {
		args = append(args, "--env-file", envFile)
	}

	imageTag := "vpscontrol-" + dep.Name
	args = append(args, imageTag)
	return args
}

// needsVolumeMount décide si le code de cette app doit être monté en
// volume dans le conteneur.
//
// Règle :
//   - React / Next / Vite / Astro / SvelteKit / Nuxt → NON (build step)
//   - Node générique → OUI seulement s'il n'y a pas de scripts.build
//   - Python / Go / Laravel / Static → OUI
//
// Le but : si l'app a un step de build (npm run build), on ne peut PAS
// monter le code source en volume, car le build doit tourner DANS l'image
// au moment du docker build.
func needsVolumeMount(dep store.Deployment) bool {
	switch dep.Stack {
	case "react", "next", "astro", "sveltekit", "nuxt":
		return false // build step obligatoire
	case "node":
		// Node générique : monter en volume seulement s'il n'y a pas de
		// step de build. Sinon c'est en réalité une app avec build.
		return !nodeHasBuildScript(appRoot(dep))
	case "python", "go", "laravel", "static":
		return true
	default:
		return false
	}
}

// nodeHasBuildScript vérifie si package.json contient un script "build".
// Si oui, on doit garder le mode classique (build dans l'image).
func nodeHasBuildScript(dir string) bool {
	b, err := os.ReadFile(dir + "/package.json")
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return false
	}
	_, ok := pkg.Scripts["build"]
	return ok
}

// containerWorkdir retourne le chemin de montage dans le conteneur
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