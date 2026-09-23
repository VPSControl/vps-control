package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"vpscontrol/internal/auth"
	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

type UserHandlers struct {
	Store *store.Store
}

// =====================================================================
// List / Create (inchangés depuis L1)
// =====================================================================

func (h *UserHandlers) List(w http.ResponseWriter, r *http.Request) {
	users := h.Store.ListUsers()
	out := make([]map[string]interface{}, 0, len(users))
	for _, u := range users {
		out = append(out, publicUser(u))
	}
	middleware.JSON(w, http.StatusOK, out)
}

type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func (h *UserHandlers) Create(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if !usernameRe.MatchString(req.Username) {
		middleware.JSONError(w, http.StatusBadRequest, "invalid username (3-32 alphanumeric characters)")
		return
	}
	if len(req.Password) < 8 {
		middleware.JSONError(w, http.StatusBadRequest, "password must be at least 8 characters long")
		return
	}
	if req.Role != "admin" && req.Role != "viewer" {
		req.Role = "viewer"
	}
	hash, salt, err := auth.HashPassword(req.Password)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	u := store.User{
		ID:           randomID(),
		Username:     req.Username,
		PasswordHash: hash,
		Salt:         salt,
		Role:         req.Role,
		CreatedAt:    time.Now(),
	}
	if err := h.Store.AddUser(u); err != nil {
		middleware.JSONError(w, http.StatusConflict, err.Error())
		return
	}
	middleware.JSON(w, http.StatusCreated, publicUser(u))
}

// =====================================================================
// Preview (L4) : GET /api/admin/users/{id}/preview
// =====================================================================

// Preview : renvoie ce qui serait supprimé si on supprime cet utilisateur.
// N'effectue aucune modification.
func (h *UserHandlers) Preview(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromPath(r.URL.Path, "/preview")
	if userID == "" {
		middleware.JSONError(w, http.StatusBadRequest, "missing user id")
		return
	}
	u, ok := h.Store.FindUserByID(userID)
	if !ok {
		middleware.JSONError(w, http.StatusNotFound, "user not found")
		return
	}
	sh := &ServerHandlers{Store: h.Store}
	preview := sh.buildUserPurgePreview(u)
	middleware.JSON(w, http.StatusOK, preview)
}

// =====================================================================
// Delete (L4) : DELETE /api/admin/users/{id}?confirm=true
// =====================================================================

// Delete : supprime un utilisateur et toutes ses ressources (serveurs,
// apps, conteneurs, images, dossiers, backups, domaines, tokens GitHub).
//
// Sécurité :
//   - refuse de supprimer son propre compte
//   - refuse de supprimer le dernier admin
//   - si l'user a des ressources et que ?confirm=true n'est pas fourni,
//     renvoie 409 Conflict avec un message demandant confirmation.
func (h *UserHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	// userIDFromPath : /api/users/{id} → {id}
	userID := userIDFromPath(r.URL.Path, "")
	if userID == "" {
		middleware.JSONError(w, http.StatusBadRequest, "missing user id")
		return
	}

	current, _ := middleware.UserFromContext(r.Context())
	if current.ID == userID {
		middleware.JSONError(w, http.StatusBadRequest, "you cannot delete your own account")
		return
	}

	u, ok := h.Store.FindUserByID(userID)
	if !ok {
		middleware.JSONError(w, http.StatusNotFound, "user not found")
		return
	}

	// Garde-fou : dernier admin.
	if u.Role == "admin" {
		adminCount := 0
		for _, other := range h.Store.ListUsers() {
			if other.Role == "admin" {
				adminCount++
			}
		}
		if adminCount <= 1 {
			middleware.JSONError(w, http.StatusBadRequest, "cannot delete the last remaining admin account")
			return
		}
	}

	// Aperçu de ce qui va être supprimé.
	sh := &ServerHandlers{Store: h.Store}
	preview := sh.buildUserPurgePreview(u)

	hasResources := preview.ServersCount > 0 || preview.AppsCount > 0
	confirm := r.URL.Query().Get("confirm") == "true"

	if hasResources && !confirm {
		// On renvoie 409 avec le preview pour que l'UI puisse afficher
		// une modale "es-tu sûr ?" détaillée.
		middleware.JSON(w, http.StatusConflict, map[string]interface{}{
			"error":   "this user owns resources. Re-send the request with ?confirm=true to delete everything.",
			"preview": preview,
		})
		return
	}

	// Purge cascade (best-effort).
	purgeResult := sh.purgeUserResources(userID)

	// Suppression de l'utilisateur lui-même (nettoie aussi ses tokens
	// API et ses subuser entries, mais pas ses serveurs/apps — c'est
	// purgeUserResources qui s'en est occupé).
	if err := h.Store.DeleteUser(userID); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "purge done but user delete failed: "+err.Error())
		return
	}

	_ = h.Store.AddActivity(store.ActivityEntry{
		ID:        randomID(),
		Timestamp: time.Now(),
		ActorID:   current.ID,
		ActorName: current.Username,
		Action:    "user.delete",
		Target:    u.Username,
		Details: map[string]interface{}{
			"servers":  purgeResult.ServersPurged,
			"apps":     purgeResult.DeploymentsPurged,
			"backups":  purgeResult.BackupsRemoved,
			"domains":  purgeResult.DomainsRemoved,
			"folders":  purgeResult.FoldersRemoved,
			"errors":   len(purgeResult.Errors),
		},
	})

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"message": "User and all associated resources deleted.",
		"purged":  purgeResult,
		"preview": preview,
	})
}

// =====================================================================
// Helpers
// =====================================================================

// userIDFromPath : extrait l'ID utilisateur depuis /api/users/{id}
// ou /api/admin/users/{id}/preview.
//
// suffix : chaîne à retirer de la fin (ex: "/preview"), "" si rien.
func userIDFromPath(path, suffix string) string {
	// On accepte les deux préfixes pour rester flexible.
	for _, prefix := range []string{"/api/admin/users/", "/api/users/"} {
		if strings.HasPrefix(path, prefix) {
			rest := strings.TrimPrefix(path, prefix)
			if suffix != "" {
				rest = strings.TrimSuffix(rest, suffix)
			}
			rest = strings.Trim(rest, "/")
			// Ignorer les sous-chemins éventuels.
			if idx := strings.Index(rest, "/"); idx >= 0 {
				rest = rest[:idx]
			}
			return rest
		}
	}
	return ""
}

// publicUser : identique à auth.go, mais dupliqué ici pour éviter une
// dépendance croisée dans le package.
//
// NOTE : si tu as déjà publicUser dans auth.go (package handlers),
// supprime cette copie — sinon conflit de déclaration. Vérifie !
func publicUser(u store.User) map[string]interface{} {
	return map[string]interface{}{
		"id":        u.ID,
		"username":  u.Username,
		"role":      u.Role,
		"createdAt": u.CreatedAt,
	}
}