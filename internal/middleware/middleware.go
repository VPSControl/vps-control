package middleware

import (
	"context"
	"encoding/json"
	"net/http"

	"vpscontrol/internal/auth"
	"vpscontrol/internal/store"
)

type ctxKey string

const userCtxKey ctxKey = "user"

const SessionCookieName = "vpscontrol_session"

// Auth builds a middleware that checks the session cookie and injects the
// current user into the request context.
func Auth(secret []byte, st *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(SessionCookieName)
			if err != nil {
				JSONError(w, http.StatusUnauthorized, "not authenticated")
				return
			}
			userID, err := auth.ParseSessionToken(secret, cookie.Value)
			if err != nil {
				JSONError(w, http.StatusUnauthorized, "invalid session, please log in again")
				return
			}
			u, ok := st.FindUserByID(userID)
			if !ok {
				JSONError(w, http.StatusUnauthorized, "user not found")
				return
			}
			ctx := context.WithValue(r.Context(), userCtxKey, u)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAdmin must be chained after Auth.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := UserFromContext(r.Context())
		if !ok || u.Role != "admin" {
			JSONError(w, http.StatusForbidden, "admins only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func UserFromContext(ctx context.Context) (store.User, bool) {
	u, ok := ctx.Value(userCtxKey).(store.User)
	return u, ok
}

func JSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func JSONError(w http.ResponseWriter, status int, message string) {
	JSON(w, status, map[string]string{"error": message})
}
