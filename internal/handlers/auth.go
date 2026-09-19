package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"vpscontrol/internal/auth"
	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,32}$`)

type AuthHandlers struct {
	Store  *store.Store
	Secret []byte
}

func (h *AuthHandlers) SetupStatus(w http.ResponseWriter, r *http.Request) {
	middleware.JSON(w, http.StatusOK, map[string]bool{
		"needsSetup": h.Store.UserCount() == 0,
	})
}

type setupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *AuthHandlers) Setup(w http.ResponseWriter, r *http.Request) {
	if h.Store.UserCount() > 0 {
		middleware.JSONError(w, http.StatusConflict, "le panel a déjà été initialisé")
		return
	}
	var req setupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "requête invalide")
		return
	}
	if !usernameRe.MatchString(req.Username) {
		middleware.JSONError(w, http.StatusBadRequest, "nom d'utilisateur invalide (3-32 caractères alphanumériques)")
		return
	}
	if len(req.Password) < 8 {
		middleware.JSONError(w, http.StatusBadRequest, "le mot de passe doit faire au moins 8 caractères")
		return
	}
	hash, salt, err := auth.HashPassword(req.Password)
	if err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	u := store.User{
		ID:           randomID(),
		Username:     req.Username,
		PasswordHash: hash,
		Salt:         salt,
		Role:         "admin",
		CreatedAt:    time.Now(),
	}
	if err := h.Store.AddUser(u); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.setSessionCookie(w, u.ID)
	middleware.JSON(w, http.StatusCreated, publicUser(u))
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *AuthHandlers) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, "requête invalide")
		return
	}
	u, ok := h.Store.FindUserByUsername(req.Username)
	if !ok || !auth.VerifyPassword(req.Password, u.PasswordHash, u.Salt) {
		middleware.JSONError(w, http.StatusUnauthorized, "identifiants incorrects")
		return
	}
	h.setSessionCookie(w, u.ID)
	middleware.JSON(w, http.StatusOK, publicUser(u))
}

func (h *AuthHandlers) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *AuthHandlers) Me(w http.ResponseWriter, r *http.Request) {
	u, _ := middleware.UserFromContext(r.Context())
	middleware.JSON(w, http.StatusOK, publicUser(u))
}

func (h *AuthHandlers) setSessionCookie(w http.ResponseWriter, userID string) {
	token := auth.CreateSessionToken(h.Secret, userID, 7*24*time.Hour)
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   false, // passer à true si servi en HTTPS direct (recommandé derrière un reverse proxy TLS)
		SameSite: http.SameSiteStrictMode,
		MaxAge:   7 * 24 * 3600,
	})
}

func publicUser(u store.User) map[string]interface{} {
	return map[string]interface{}{
		"id":        u.ID,
		"username":  u.Username,
		"role":      u.Role,
		"createdAt": u.CreatedAt,
	}
}
