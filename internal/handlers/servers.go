package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

type ServerHandlers struct {
	Store *store.Store
}

// =====================================================================
// Helpers
// =====================================================================

// enrichServer : ajoute les infos owner + usage à un Server pour l'UI.
func (h *ServerHandlers) enrichServer(srv store.Server) map[string]interface{} {
	ownerName := ""
	if u, ok := h.Store.FindUserByID(srv.OwnerID); ok {
		ownerName = u.Username
	}

	appsCount := h.Store.CountAppsInServer(srv.ID)
	diskUsed := h.Store.DiskUsedByServer(srv.ID)

	return map[string]interface{}{
		"id":         srv.ID,
		"name":       srv.Name,
		"ownerId":    srv.OwnerID,
		"ownerName":  ownerName,
		"diskMB":     srv.DiskMB,
		"memoryMB":   srv.MemoryMB,
		"cpuQuota":   srv.CPUQuota,
		"maxApps":    srv.MaxApps,
		"notes":      srv.Notes,
		"createdBy":  srv.CreatedBy,
		"createdAt":  srv.CreatedAt,
		"appsCount":  appsCount,
		"diskUsedMB": diskUsed,
	}
}

// checkServerQuota : vérifie qu'on peut ajouter UNE app à ce serveur.
// Retourne (ok, message d'erreur).
//
// Règles :
//   - si MaxApps > 0 et qu'on a déjà atteint la limite → refus
//   - si DiskMB > 0 et que le disque utilisé dépasse déjà le quota → refus
//     (on ne peut pas deviner la taille de la nouvelle app avant le build,
//     donc on refuse seulement si le quota est DÉJÀ dépassé)
func (h *ServerHandlers) checkServerQuota(serverID string) (bool, string) {
	srv, ok := h.Store.FindServerByID(serverID)
	if !ok {
		return false, "server not found"
	}
	if srv.MaxApps > 0 {
		current := h.Store.CountAppsInServer(serverID)
		if current >= srv.MaxApps {
			return false, fmt.Sprintf("server %q has reached its app limit (%d/%d)", srv.Name, current, srv.MaxApps)
		}
	}
	if srv.DiskMB > 0 {
		used := h.Store.DiskUsedByServer(serverID)
		if used >= int64(srv.DiskMB) {
			return false, fmt.Sprintf("server %q disk quota exceeded (%d/%d MB)", srv.Name, used, srv.DiskMB)
		}
	}
	return true, ""
}

// resolveServerID : prend un serverID fourni par le client, vérifie que
// l'utilisateur y a accès (owner uniquement — isolation stricte), et
// retourne l'ID validé.
//
// Règle 4 = B : même un admin ne peut déployer que sur SES propres
// serveurs. Pour déployer sur le serveur d'un autre user, il faut être
// invité comme subuser de l'app (mais on ne partage pas les serveurs).
func (h *ServerHandlers) resolveServerID(user store.User, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", fmt.Errorf("server is required")
	}
	srv, ok := h.Store.FindServerByID(requested)
	if !ok {
		return "", fmt.Errorf("server not found")
	}
	if srv.OwnerID != user.ID {
		return "", fmt.Errorf("you don't own this server")
	}
	return srv.ID, nil
}

// =====================================================================
// Routes USER : /api/servers/mine
// =====================================================================

// Mine : GET /api/servers/mine
func (h *ServerHandlers) Mine(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	servers := h.Store.ListServersForUser(user.ID)
	out := make([]map[string]interface{}, 0, len(servers))
	for _, srv := range servers {
		out = append(out, h.enrichServer(srv))
	}
	middleware.JSON(w, http.StatusOK, out)
}

// =====================================================================
// Routes ADMIN : /api/admin/servers
// =====================================================================

// ListAll : GET /api/admin/servers
func (h *ServerHandlers) ListAll(w http.ResponseWriter, r *http.Request) {
	servers := h.Store.ListServers()
	out := make([]map[string]interface{}, 0, len(servers))
	for _, srv := range servers {
		out = append(out, h.enrichServer(srv))
	}
	middleware.JSON(w, http.StatusOK, out)
}

// Create : POST /api/admin/servers
func (h *ServerHandlers) Create(w http.ResponseWriter, r *http.Request) {
	actor, _ := middleware.UserFromContext(r.Context())

	var req struct {
		Name     string  `json:"name"`
		OwnerID  string  `json:"ownerId"`
		DiskMB   int     `json:"diskMB"`
		MemoryMB int     `json:"memoryMB"`
		CPUQuota float64 `json:"cpuQuota"`
		MaxApps  int     `json:"maxApps"`
		Notes    string  `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		middleware.JSONError(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(req.Name) > 60 {
		middleware.JSONError(w, http.StatusBadRequest, "name too long (max 60 chars)")
		return
	}
	if req.OwnerID == "" {
		middleware.JSONError(w, http.StatusBadRequest, "ownerId is required")
		return
	}
	if _, ok := h.Store.FindUserByID(req.OwnerID); !ok {
		middleware.JSONError(w, http.StatusBadRequest, "owner user not found")
		return
	}
	if req.DiskMB < 0 || req.DiskMB > 10*1024*1024 {
		middleware.JSONError(w, http.StatusBadRequest, "diskMB out of range (0 to 10 TB)")
		return
	}
	if req.MemoryMB < 0 || req.MemoryMB > 1024*1024 {
		middleware.JSONError(w, http.StatusBadRequest, "memoryMB out of range (0 to 1 TB)")
		return
	}
	if req.CPUQuota < 0 || req.CPUQuota > 100000 {
		middleware.JSONError(w, http.StatusBadRequest, "cpuQuota out of range (0 to 100000%)")
		return
	}
	if req.MaxApps < 0 || req.MaxApps > 10000 {
		middleware.JSONError(w, http.StatusBadRequest, "maxApps out of range (0 to 10000)")
		return
	}

	srv := store.Server{
		ID:        randomID(),
		Name:      req.Name,
		OwnerID:   req.OwnerID,
		DiskMB:    req.DiskMB,
		MemoryMB:  req.MemoryMB,
		CPUQuota:  req.CPUQuota,
		MaxApps:   req.MaxApps,
		Notes:     strings.TrimSpace(req.Notes),
		CreatedBy: actor.ID,
		CreatedAt: time.Now(),
	}
	if err := h.Store.AddServer(srv); err != nil {
		middleware.JSONError(w, http.StatusConflict, err.Error())
		return
	}

	_ = h.Store.AddActivity(store.ActivityEntry{
		ID:        randomID(),
		Timestamp: time.Now(),
		ActorID:   actor.ID,
		ActorName: actor.Username,
		Action:    "server.create",
		Target:    srv.Name,
		Details: map[string]interface{}{
			"ownerId": srv.OwnerID,
			"diskMB":  srv.DiskMB,
			"maxApps": srv.MaxApps,
		},
	})

	middleware.JSON(w, http.StatusCreated, h.enrichServer(srv))
}

// Update : PUT /api/admin/servers/{id}
func (h *ServerHandlers) Update(w http.ResponseWriter, r *http.Request) {
	actor, _ := middleware.UserFromContext(r.Context())
	id := serverIDFromPath(r.URL.Path)
	if id == "" {
		middleware.JSONError(w, http.StatusBadRequest, "missing server id")
		return
	}

	srv, ok := h.Store.FindServerByID(id)
	if !ok {
		middleware.JSONError(w, http.StatusNotFound, "server not found")
		return
	}

	var req struct {
		Name     *string  `json:"name"`
		OwnerID  *string  `json:"ownerId"`
		DiskMB   *int     `json:"diskMB"`
		MemoryMB *int     `json:"memoryMB"`
		CPUQuota *float64 `json:"cpuQuota"`
		MaxApps  *int     `json:"maxApps"`
		Notes    *string  `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}

	changed := false

	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			middleware.JSONError(w, http.StatusBadRequest, "name cannot be empty")
			return
		}
		if len(name) > 60 {
			middleware.JSONError(w, http.StatusBadRequest, "name too long")
			return
		}
		if name != srv.Name {
			srv.Name = name
			changed = true
		}
	}
	if req.OwnerID != nil {
		newOwner := strings.TrimSpace(*req.OwnerID)
		if newOwner == "" {
			middleware.JSONError(w, http.StatusBadRequest, "ownerId cannot be empty")
			return
		}
		if _, ok := h.Store.FindUserByID(newOwner); !ok {
			middleware.JSONError(w, http.StatusBadRequest, "owner user not found")
			return
		}
		if newOwner != srv.OwnerID {
			// Changer le propriétaire : on met aussi à jour OwnerID de
			// tous les déploiements rattachés, sinon ils deviennent
			// inaccessibles au nouveau propriétaire.
			deps := h.Store.ListDeploymentsInServer(srv.ID)
			for _, d := range deps {
				d.OwnerID = newOwner
				_ = h.Store.UpdateDeployment(d)
			}
			srv.OwnerID = newOwner
			changed = true
		}
	}
	if req.DiskMB != nil {
		if *req.DiskMB < 0 || *req.DiskMB > 10*1024*1024 {
			middleware.JSONError(w, http.StatusBadRequest, "diskMB out of range")
			return
		}
		if *req.DiskMB != srv.DiskMB {
			srv.DiskMB = *req.DiskMB
			changed = true
		}
	}
	if req.MemoryMB != nil {
		if *req.MemoryMB < 0 || *req.MemoryMB > 1024*1024 {
			middleware.JSONError(w, http.StatusBadRequest, "memoryMB out of range")
			return
		}
		if *req.MemoryMB != srv.MemoryMB {
			srv.MemoryMB = *req.MemoryMB
			changed = true
		}
	}
	if req.CPUQuota != nil {
		if *req.CPUQuota < 0 || *req.CPUQuota > 100000 {
			middleware.JSONError(w, http.StatusBadRequest, "cpuQuota out of range")
			return
		}
		if *req.CPUQuota != srv.CPUQuota {
			srv.CPUQuota = *req.CPUQuota
			changed = true
		}
	}
	if req.MaxApps != nil {
		if *req.MaxApps < 0 || *req.MaxApps > 10000 {
			middleware.JSONError(w, http.StatusBadRequest, "maxApps out of range")
			return
		}
		if *req.MaxApps != srv.MaxApps {
			srv.MaxApps = *req.MaxApps
			changed = true
		}
	}
	if req.Notes != nil {
		notes := strings.TrimSpace(*req.Notes)
		if notes != srv.Notes {
			srv.Notes = notes
			changed = true
		}
	}

	if !changed {
		middleware.JSON(w, http.StatusOK, map[string]interface{}{
			"message": "No changes detected.",
			"changed": false,
			"server":  h.enrichServer(srv),
		})
		return
	}

	if err := h.Store.UpdateServer(srv); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = h.Store.AddActivity(store.ActivityEntry{
		ID:        randomID(),
		Timestamp: time.Now(),
		ActorID:   actor.ID,
		ActorName: actor.Username,
		Action:    "server.update",
		Target:    srv.Name,
	})

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"message": "Server updated.",
		"changed": true,
		"server":  h.enrichServer(srv),
	})
}

// Delete : DELETE /api/admin/servers/{id}?mode=delete|move&target=<serverID>&confirm=true
//
// Deux modes (règle 6 = A+C) :
//   - mode=delete (défaut) : supprime les apps du serveur (conteneurs +
//     dossiers + entrées store). Action irréversible. Requiert confirm=true
//     si le serveur contient au moins une app.
//   - mode=move&target=<id> : déplace toutes les apps vers un autre
//     serveur, puis supprime le serveur vide.
//
// Retourne { deleted, moved, errors: [] }.
func (h *ServerHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	actor, _ := middleware.UserFromContext(r.Context())
	id := serverIDFromPath(r.URL.Path)
	if id == "" {
		middleware.JSONError(w, http.StatusBadRequest, "missing server id")
		return
	}

	srv, ok := h.Store.FindServerByID(id)
	if !ok {
		middleware.JSONError(w, http.StatusNotFound, "server not found")
		return
	}

	deps := h.Store.ListDeploymentsInServer(id)

	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "delete"
	}

	// Si le serveur a des apps et qu'aucun confirm n'est donné, on refuse
	// pour éviter les suppressions accidentelles.
	if len(deps) > 0 && mode == "delete" && r.URL.Query().Get("confirm") != "true" {
		middleware.JSONError(w, http.StatusConflict,
			fmt.Sprintf("this server contains %d app(s). Add ?confirm=true to delete them, or use ?mode=move&target=<serverId>", len(deps)))
		return
	}

	if mode == "move" {
		targetID := strings.TrimSpace(r.URL.Query().Get("target"))
		if targetID == "" {
			middleware.JSONError(w, http.StatusBadRequest, "target server id is required for move mode")
			return
		}
		if targetID == id {
			middleware.JSONError(w, http.StatusBadRequest, "target server must be different")
			return
		}
		target, ok := h.Store.FindServerByID(targetID)
		if !ok {
			middleware.JSONError(w, http.StatusBadRequest, "target server not found")
			return
		}
		if target.OwnerID != srv.OwnerID {
			middleware.JSONError(w, http.StatusBadRequest, "target server belongs to a different owner")
			return
		}
		moved := 0
		for _, d := range deps {
			d.ServerID = targetID
			if err := h.Store.UpdateDeployment(d); err == nil {
				moved++
			}
		}
		if err := h.Store.DeleteServer(id); err != nil {
			middleware.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = h.Store.AddActivity(store.ActivityEntry{
			ID:        randomID(),
			Timestamp: time.Now(),
			ActorID:   actor.ID,
			ActorName: actor.Username,
			Action:    "server.delete",
			Target:    srv.Name,
			Details:   map[string]interface{}{"mode": "move", "moved": moved, "target": target.Name},
		})
		middleware.JSON(w, http.StatusOK, map[string]interface{}{
			"moved":  moved,
			"errors": []string{},
		})
		return
	}

	// mode=delete : supprimer chaque app (conteneur + dossier + store).
	deleted := 0
	errs := []string{}
	for _, d := range deps {
		// 1. Arrêter et supprimer le conteneur
		if d.Container != "" {
			_, _ = runCommand(30*time.Second, "docker", "rm", "-f", d.Container)
		}
		// 2. Supprimer le dossier
		if d.Path != "" {
			if err := removeAllSafe(d.Path); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", d.Name, err))
				continue
			}
		}
		// 3. Supprimer du store
		if err := h.Store.DeleteDeployment(d.ID); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", d.Name, err))
			continue
		}
		deleted++
	}

	if err := h.Store.DeleteServer(id); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = h.Store.AddActivity(store.ActivityEntry{
		ID:        randomID(),
		Timestamp: time.Now(),
		ActorID:   actor.ID,
		ActorName: actor.Username,
		Action:    "server.delete",
		Target:    srv.Name,
		Details:   map[string]interface{}{"mode": "delete", "deleted": deleted},
	})

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"deleted": deleted,
		"errors":  errs,
	})
}

// =====================================================================
// Helpers
// =====================================================================

// serverIDFromPath : extrait l'id depuis /api/admin/servers/{id}
func serverIDFromPath(path string) string {
	const prefix = "/api/admin/servers/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	trimmed := strings.TrimPrefix(path, prefix)
	trimmed = strings.Trim(trimmed, "/")
	if idx := strings.Index(trimmed, "/"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	return trimmed
}

// removeAllSafe : os.RemoveAll avec une vérification que le chemin est
// bien sous /opt/vpscontrol/apps. Refuse les chemins vides, la racine,
// ou tout chemin hors de la zone de déploiement.
func removeAllSafe(path string) error {
	path = strings.TrimSpace(path)
	if path == "" || path == "/" {
		return fmt.Errorf("refusing to delete empty or root path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	allowed := "/opt/vpscontrol/apps"
	if abs != allowed && !strings.HasPrefix(abs, allowed+"/") {
		return fmt.Errorf("refusing to delete path outside of %s (got %s)", allowed, abs)
	}
	return os.RemoveAll(abs)
}