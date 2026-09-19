package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"vpscontrol/internal/middleware"
)

type ServiceHandlers struct{}

type dockerPs struct {
	Names   string `json:"Names"`
	Image   string `json:"Image"`
	Status  string `json:"Status"`
	Ports   string `json:"Ports"`
	State   string `json:"State"`
	ID      string `json:"ID"`
	Command string `json:"Command"`
}

// List retourne l'état de tous les conteneurs Docker (= tous les services
// déployés depuis le panel, plus tout autre conteneur présent sur le VPS).
func (h *ServiceHandlers) List(w http.ResponseWriter, r *http.Request) {
	out, err := runCommand(10*time.Second, "docker", "ps", "-a", "--format", "{{json .}}")
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "docker indisponible: "+out)
		return
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	services := make([]dockerPs, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var p dockerPs
		if err := json.Unmarshal([]byte(line), &p); err == nil {
			services = append(services, p)
		}
	}
	middleware.JSON(w, http.StatusOK, services)
}

var containerNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func validContainerName(name string) bool {
	return containerNameRe.MatchString(name)
}

func nameFromPath(prefix, path string) string {
	return strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/")
}

func (h *ServiceHandlers) Start(w http.ResponseWriter, r *http.Request) {
	h.action(w, r, "/api/services/", "start", "start")
}
func (h *ServiceHandlers) Stop(w http.ResponseWriter, r *http.Request) {
	h.action(w, r, "/api/services/", "stop", "stop")
}
func (h *ServiceHandlers) Restart(w http.ResponseWriter, r *http.Request) {
	h.action(w, r, "/api/services/", "restart", "restart")
}

func (h *ServiceHandlers) action(w http.ResponseWriter, r *http.Request, prefix, suffix, dockerVerb string) {
	name := nameFromPath(prefix, strings.TrimSuffix(r.URL.Path, "/"+suffix))
	if !validContainerName(name) {
		middleware.JSONError(w, http.StatusBadRequest, "nom de service invalide")
		return
	}
	out, err := runCommand(30*time.Second, "docker", dockerVerb, name)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, out)
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *ServiceHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	name := nameFromPath("/api/services/", r.URL.Path)
	if !validContainerName(name) {
		middleware.JSONError(w, http.StatusBadRequest, "nom de service invalide")
		return
	}
	out, err := runCommand(30*time.Second, "docker", "rm", "-f", name)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, out)
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *ServiceHandlers) Logs(w http.ResponseWriter, r *http.Request) {
	name := nameFromPath("/api/services/", strings.TrimSuffix(r.URL.Path, "/logs"))
	if !validContainerName(name) {
		middleware.JSONError(w, http.StatusBadRequest, "nom de service invalide")
		return
	}
	out, err := runCommand(10*time.Second, "docker", "logs", "--tail", "300", name)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, out)
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]string{"logs": out})
}

// CreateDatabaseService lance rapidement un conteneur MySQL ou Postgres,
// pratique pour donner une base à une app tout juste déployée (ex: Laravel).
type createDBServiceRequest struct {
	Name     string `json:"name"`
	Engine   string `json:"engine"` // "mysql" ou "postgres"
	DBName   string `json:"dbName"`
	User     string `json:"user"`
	Password string `json:"password"`
	Port     string `json:"port"`
}

func (h *ServiceHandlers) CreateDatabaseService(w http.ResponseWriter, r *http.Request) {
	var req createDBServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "requête invalide")
		return
	}
	if !validContainerName(req.Name) {
		middleware.JSONError(w, http.StatusBadRequest, "nom invalide")
		return
	}
	var args []string
	containerName := "db-" + req.Name
	switch req.Engine {
	case "mysql":
		args = []string{"run", "-d", "--name", containerName, "--restart", "unless-stopped",
			"-e", "MYSQL_ROOT_PASSWORD=" + req.Password,
			"-e", "MYSQL_DATABASE=" + req.DBName,
			"-e", "MYSQL_USER=" + req.User,
			"-e", "MYSQL_PASSWORD=" + req.Password,
			"-p", req.Port + ":3306",
			"mysql:8"}
	case "postgres":
		args = []string{"run", "-d", "--name", containerName, "--restart", "unless-stopped",
			"-e", "POSTGRES_DB=" + req.DBName,
			"-e", "POSTGRES_USER=" + req.User,
			"-e", "POSTGRES_PASSWORD=" + req.Password,
			"-p", req.Port + ":5432",
			"postgres:16-alpine"}
	default:
		middleware.JSONError(w, http.StatusBadRequest, "moteur non supporté (mysql ou postgres)")
		return
	}
	out, err := runCommand(60*time.Second, "docker", args...)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, out)
		return
	}
	middleware.JSON(w, http.StatusCreated, map[string]string{"container": containerName})
}
