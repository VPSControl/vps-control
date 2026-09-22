package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

type SubuserHandlers struct {
	Store *store.Store
}

// Toutes les permissions connues (whitelist stricte).
var validPermissions = map[string]bool{
	"console":     true,
	"files.read":  true,
	"files.write": true,
	"backup":      true,
	"settings":    true,
}

func filterValidPermissions(input []string) []string {
	out := []string{}
	for _, p := range input {
		if validPermissions[p] {
			out = append(out, p)
		}
	}
	return out
}

// generateInviteToken : 24 bytes aléatoires en hex (48 chars).
func generateInviteToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- Invitations ----

// Invite : POST /api/deployments/{id}/invite
// Crée un lien d'invitation pour un déploiement.
func (h *SubuserHandlers) Invite(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserFromContext(r.Context())
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	// Seuls le propriétaire et les admins peuvent inviter.
	if user.Role != "admin" && dep.OwnerID != user.ID {
		middleware.JSONError(w, http.StatusForbidden, "only the owner can invite collaborators")
		return
	}

	var req struct {
		Permissions []string `json:"permissions"`
		ExpiresIn   int      `json:"expiresInHours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}

	perms := filterValidPermissions(req.Permissions)
	if len(perms) == 0 {
		middleware.JSONError(w, http.StatusBadRequest, "at least one permission is required")
		return
	}
	if req.ExpiresIn <= 0 || req.ExpiresIn > 24*30 {
		req.ExpiresIn = 24
	}

	inv := store.Invitation{
		Token:        generateInviteToken(),
		DeploymentID: dep.ID,
		Permissions:  perms,
		CreatedBy:    user.ID,
		ExpiresAt:    time.Now().Add(time.Duration(req.ExpiresIn) * time.Hour),
	}
	if err := h.Store.AddInvitation(inv); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	link := scheme + "://" + host + "/invite/" + inv.Token

	middleware.JSON(w, http.StatusCreated, map[string]interface{}{
		"inviteLink":  link,
		"token":       inv.Token,
		"expiresAt":   inv.ExpiresAt.Format(time.RFC3339),
		"permissions": perms,
	})
}

// ListInvitations : GET /api/deployments/{id}/invitations
func (h *SubuserHandlers) ListInvitations(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}
	invs := h.Store.ListInvitationsForDeployment(dep.ID)
	// Ne pas exposer le token complet dans la liste
	out := []map[string]interface{}{}
	for _, inv := range invs {
		out = append(out, map[string]interface{}{
			"tokenPrefix": inv.Token[:8],
			"permissions": inv.Permissions,
			"expiresAt":   inv.ExpiresAt,
			"createdBy":   inv.CreatedBy,
		})
	}
	middleware.JSON(w, http.StatusOK, out)
}

// AcceptInvite : POST /api/invite/{token}/accept
// Rejoint un déploiement via un token. Accessible à tout utilisateur authentifié.
func (h *SubuserHandlers) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserFromContext(r.Context())

	token := strings.TrimPrefix(r.URL.Path, "/api/invite/")
	token = strings.TrimSuffix(token, "/accept")
	if token == "" {
		middleware.JSONError(w, http.StatusBadRequest, "missing token")
		return
	}

	inv, ok := h.Store.FindInvitation(token)
	if !ok {
		middleware.JSONError(w, http.StatusNotFound, "invitation not found")
		return
	}
	if inv.Used {
		middleware.JSONError(w, http.StatusGone, "invitation already used")
		return
	}
	if time.Now().After(inv.ExpiresAt) {
		middleware.JSONError(w, http.StatusGone, "invitation expired")
		return
	}

	dep, ok := h.Store.FindDeploymentByID(inv.DeploymentID)
	if !ok {
		middleware.JSONError(w, http.StatusNotFound, "deployment not found")
		return
	}

	// Si l'utilisateur est déjà propriétaire, on accepte quand même mais
	// on ne crée pas de subuser (il a déjà tous les droits).
	if dep.OwnerID == user.ID {
		h.Store.MarkInvitationUsed(token)
		middleware.JSON(w, http.StatusOK, map[string]interface{}{
			"deployment":  dep,
			"permissions": []string{"*"},
			"alreadyOwner": true,
		})
		return
	}

	// Vérifier qu'il n'est pas déjà subuser
	if _, ok := h.Store.FindSubuser(user.ID, dep.ID); ok {
		h.Store.MarkInvitationUsed(token)
		middleware.JSON(w, http.StatusOK, map[string]interface{}{
			"deployment": dep,
			"alreadySubuser": true,
		})
		return
	}

	sub := store.Subuser{
		ID:           randomID(),
		UserID:       user.ID,
		DeploymentID: dep.ID,
		Permissions:  inv.Permissions,
		InvitedBy:    inv.CreatedBy,
		CreatedAt:    time.Now(),
	}
	if err := h.Store.AddSubuser(sub); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.Store.MarkInvitationUsed(token)

	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"deployment":  dep,
		"permissions": inv.Permissions,
	})
}

// ---- Subusers ----

// List : GET /api/deployments/{id}/subusers
func (h *SubuserHandlers) List(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	subs := h.Store.ListSubusersForDeployment(dep.ID)
	out := []map[string]interface{}{}
	for _, sub := range subs {
		u, _ := h.Store.FindUserByID(sub.UserID)
		out = append(out, map[string]interface{}{
			"id":          sub.ID,
			"userId":      sub.UserID,
			"username":    u.Username,
			"permissions": sub.Permissions,
			"invitedBy":   sub.InvitedBy,
			"createdAt":   sub.CreatedAt,
		})
	}
	middleware.JSON(w, http.StatusOK, out)
}

// UpdatePermissions : PUT /api/deployments/{id}/subusers/{subId}
func (h *SubuserHandlers) UpdatePermissions(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	subID := strings.TrimPrefix(r.URL.Path, "/api/deployments/"+dep.ID+"/subusers/")
	if subID == "" {
		middleware.JSONError(w, http.StatusBadRequest, "missing subuser id")
		return
	}

	var req struct {
		Permissions []string `json:"permissions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}

	perms := filterValidPermissions(req.Permissions)
	if err := h.Store.UpdateSubuserPermissions(subID, perms); err != nil {
		middleware.JSONError(w, http.StatusNotFound, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"permissions": perms,
	})
}

// Remove : DELETE /api/deployments/{id}/subusers/{subId}
func (h *SubuserHandlers) Remove(w http.ResponseWriter, r *http.Request) {
	dep, ok := middleware.DeploymentFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusBadRequest, "no deployment in context")
		return
	}

	subID := strings.TrimPrefix(r.URL.Path, "/api/deployments/"+dep.ID+"/subusers/")
	if subID == "" {
		middleware.JSONError(w, http.StatusBadRequest, "missing subuser id")
		return
	}
	if err := h.Store.DeleteSubuser(subID); err != nil {
		middleware.JSONError(w, http.StatusNotFound, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}