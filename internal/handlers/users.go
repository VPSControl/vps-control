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
// List / Create
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

func (h *UserHandlers) Delete(w http.ResponseWriter, r *http.Request) {
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

	sh := &ServerHandlers{Store: h.Store}
	preview := sh.buildUserPurgePreview(u)

	hasResources := preview.ServersCount > 0 || preview.AppsCount > 0
	confirm := r.URL.Query().Get("confirm") == "true"

	if hasResources && !confirm {
		middleware.JSON(w, http.StatusConflict, map[string]interface{}{
			"error":   "this user owns resources. Re-send the request with ?confirm=true to delete everything.",
			"preview": preview,
		})
		return
	}

	purgeResult := sh.purgeUserResources(userID)

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
			"servers": purgeResult.ServersPurged,
			"apps":    purgeResult.DeploymentsPurged,
			"backups": purgeResult.BackupsRemoved,
			"domains": purgeResult.DomainsRemoved,
			"folders": purgeResult.FoldersRemoved,
			"errors":  len(purgeResult.Errors),
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
func userIDFromPath(path, suffix string) string {
	for _, prefix := range []string{"/api/admin/users/", "/api/users/"} {
		if strings.HasPrefix(path, prefix) {
			rest := strings.TrimPrefix(path, prefix)
			if suffix != "" {
				rest = strings.TrimSuffix(rest, suffix)
			}
			rest = strings.Trim(rest, "/")
			if idx := strings.Index(rest, "/"); idx >= 0 {
				rest = rest[:idx]
			}
			return rest
		}
	}
	return ""
}

// Note : publicUser est définie dans auth.go (même package handlers).
// On ne la redéfinit PAS ici pour éviter le conflit de déclaration.