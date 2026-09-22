package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"vpscontrol/internal/store"
)

// APITokenAuth : accepte soit un cookie de session, soit un header
// `Authorization: Bearer vcp_...`.
func APITokenAuth(st *store.Store, fallback func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer vcp_") {
				token := strings.TrimPrefix(authHeader, "Bearer ")
				h := sha256.Sum256([]byte(token))
				hash := hex.EncodeToString(h[:])

				t, ok := st.FindAPITokenByHash(hash)
				if !ok {
					JSONError(w, http.StatusUnauthorized, "invalid API token")
					return
				}
				user, ok := st.FindUserByID(t.UserID)
				if !ok {
					JSONError(w, http.StatusUnauthorized, "token owner not found")
					return
				}
				_ = st.TouchAPIToken(t.ID)

				ctx := contextWithUser(r, user)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			fallback(next).ServeHTTP(w, r)
		})
	}
}