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

func (h *UserHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/users/")
	if id == "" {
		middleware.JSONError(w, http.StatusBadRequest, "missing id")
		return
	}
	current, _ := middleware.UserFromContext(r.Context())
	if current.ID == id {
		middleware.JSONError(w, http.StatusBadRequest, "you cannot delete your own account")
		return
	}
	if err := h.Store.DeleteUser(id); err != nil {
		middleware.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}
