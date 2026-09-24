// Package handlers — gestion du profil de l'utilisateur connecté.
//
// Permet à n'importe quel utilisateur (admin ou viewer) de :
//   - changer son nom d'utilisateur
//   - changer son mot de passe (SANS avoir besoin de l'ancien)
//
// NOTE : l'accès à ces endpoints est protégé par la session en cours.
// Si quelqu'un a déjà ta session, il peut changer ton mot de passe.
// C'est un choix : l'utilisateur qui a oublié son mot de passe peut le
// changer depuis son panel sans passer par le CLI.
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

type ProfileHandlers struct {
	Store *store.Store
}

type updateProfileRequest struct {
	Username    string `json:"username"`
	NewPassword string `json:"newPassword"`
}

// Update : PUT /api/profile
//
// Body :
//   { "username": "nouveau_nom" }              → change le username
//   { "newPassword": "nouveau_mdp" }           → change le mot de passe
//   { "username": "...", "newPassword": "..." } → change les deux
func (h *ProfileHandlers) Update(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		middleware.JSONError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var req updateProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "invalid request")
		return
	}

	changed := false

	// ---- Changement de username ----
	if strings.TrimSpace(req.Username) != "" && req.Username != user.Username {
		newUsername := strings.TrimSpace(req.Username)
		if !usernameRe.MatchString(newUsername) {
			middleware.JSONError(w, http.StatusBadRequest, "invalid username (3-32 alphanumeric characters, underscores, dots, dashes)")
			return
		}
		if err := h.Store.UpdateUsername(user.ID, newUsername); err != nil {
			middleware.JSONError(w, http.StatusConflict, err.Error())
			return
		}
		user.Username = newUsername
		changed = true
	}

	// ---- Changement de mot de passe ----
	// Pas de vérification de l'ancien mot de passe : c'est volontaire.
	// L'utilisateur est déjà authentifié (session valide), et on veut
	// qu'il puisse changer son mot de passe même s'il l'a oublié.
	if req.NewPassword != "" {
		if len(req.NewPassword) < 8 {
			middleware.JSONError(w, http.StatusBadRequest, "new password must be at least 8 characters")
			return
		}
		hash, salt, err := auth.HashPassword(req.NewPassword)
		if err != nil {
			middleware.JSONError(w, http.StatusInternalServerError, "hashing failed")
			return
		}
		if err := h.Store.ChangePassword(user.ID, hash, salt); err != nil {
			middleware.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		changed = true
	}

	if !changed {
		middleware.JSON(w, http.StatusOK, map[string]interface{}{
			"message": "No changes detected.",
			"changed": false,
		})
		return
	}

	// Journaliser l'action
	_ = h.Store.AddActivity(store.ActivityEntry{
		ID:        randomID(),
		Timestamp: time.Now(),
		ActorID:   user.ID,
		ActorName: user.Username,
		Action:    "user.profile.update",
	})

	// Recharger l'utilisateur pour renvoyer les valeurs à jour
	fresh, _ := h.Store.FindUserByID(user.ID)
	middleware.JSON(w, http.StatusOK, map[string]interface{}{
		"message": "Profile updated.",
		"changed": true,
		"user": map[string]interface{}{
			"id":        fresh.ID,
			"username":  fresh.Username,
			"role":      fresh.Role,
			"createdAt": fresh.CreatedAt,
		},
	})
}