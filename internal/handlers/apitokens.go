package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"vpscontrol/internal/middleware"
	"vpscontrol/internal/store"
)

type APITokenHandlers struct {
	Store *store.Store
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func generateToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "vcp_" + hex.EncodeToString(b)
}

func (h *APITokenHandlers) List(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserFromContext(r.Context())
	tokens := h.Store.ListAPITokensForUser(user.ID)
	out := []map[string]interface{}{}
	for _, t := range tokens {
		out = append(out, map[string]interface{}{
			"id":        t.ID,
			"name":      t.Name,
			"prefix":    t.Prefix,
			"createdAt": t.CreatedAt,
			"lastUsed":  t.LastUsed,
			"expiresAt": t.ExpiresAt,
			"revoked":   t.Revoked,
		})
	}
	middleware.JSON(w, http.StatusOK, out)
}

func (h *APITokenHandlers) Create(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserFromContext(r.Context())
	var req struct {
		Name       string `json:"name"`
		ExpiresInH int    `json:"expiresInHours"`
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
	if len(req.Name) > 80 {
		middleware.JSONError(w, http.StatusBadRequest, "name too long")
		return
	}

	token := generateToken()
	prefix := token[:8]
	t := store.APIToken{
		ID:        randomID(),
		UserID:    user.ID,
		Name:      req.Name,
		TokenHash: hashToken(token),
		Prefix:    prefix,
		CreatedAt: time.Now(),
	}
	if req.ExpiresInH > 0 {
		t.ExpiresAt = time.Now().Add(time.Duration(req.ExpiresInH) * time.Hour)
	}
	if err := h.Store.AddAPIToken(t); err != nil {
		middleware.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	middleware.JSON(w, http.StatusCreated, map[string]interface{}{
		"id":        t.ID,
		"name":      t.Name,
		"prefix":    t.Prefix,
		"token":     token,
		"createdAt": t.CreatedAt,
		"expiresAt": t.ExpiresAt,
	})
}

func (h *APITokenHandlers) Revoke(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/tokens/")
	if id == "" {
		middleware.JSONError(w, http.StatusBadRequest, "missing token id")
		return
	}
	if err := h.Store.RevokeAPIToken(id); err != nil {
		middleware.JSONError(w, http.StatusNotFound, err.Error())
		return
	}
	middleware.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}