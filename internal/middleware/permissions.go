package middleware

import (
	"context"
	"net/http"
	"strings"

	"vpscontrol/internal/store"
)

type permKey string

const permsCtxKey permKey = "deploymentPermissions"
const depCtxKey permKey = "deployment"

// ExtractIDFromPath : récupère le {id} dans /api/deployments/{id}/...
func ExtractIDFromPath(path string) string {
	trimmed := strings.TrimPrefix(path, "/api/deployments/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

// HasPermission : vérifie une permission dans une liste.
func HasPermission(perms []string, needed string) bool {
	for _, p := range perms {
		if p == needed {
			return true
		}
		if needed == "files.read" && p == "files.write" {
			return true
		}
		if strings.HasPrefix(needed, p+".") {
			return true
		}
	}
	return false
}

// RequireDeploymentAccess : vérifie que l'utilisateur a accès au déploiement.
//
// Règles (isolation stricte) :
//   - Le propriétaire du déploiement → accès total (["*"])
//   - Un subuser (invitation acceptée) → accès selon ses permissions
//   - Tout le monde d'autre, Y COMPRIS LES ADMINS → accès refusé
//
// Un admin peut tout de même accéder à une app s'il en est propriétaire
// ou s'il a été invité comme collaborateur.
func RequireDeploymentAccess(st *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := UserFromContext(r.Context())
			if !ok {
				JSONError(w, http.StatusUnauthorized, "not authenticated")
				return
			}

			id := ExtractIDFromPath(r.URL.Path)
			if id == "" {
				JSONError(w, http.StatusBadRequest, "missing deployment id")
				return
			}

			dep, ok := st.FindDeploymentByID(id)
			if !ok {
				JSONError(w, http.StatusNotFound, "deployment not found")
				return
			}

			var perms []string
			if dep.OwnerID == user.ID {
				perms = []string{"*"}
			} else {
				sub, ok := st.FindSubuser(user.ID, id)
				if !ok {
					JSONError(w, http.StatusForbidden, "you don't have access to this deployment")
					return
				}
				perms = sub.Permissions
			}

			ctx := context.WithValue(r.Context(), permsCtxKey, perms)
			ctx = context.WithValue(ctx, depCtxKey, dep)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequirePermission : doit être chaîné APRÈS RequireDeploymentAccess.
func RequirePermission(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			perms, ok := PermissionsFromContext(r.Context())
			if !ok {
				JSONError(w, http.StatusForbidden, "no permissions")
				return
			}
			for _, p := range perms {
				if p == "*" {
					next.ServeHTTP(w, r)
					return
				}
			}
			if !HasPermission(perms, perm) {
				JSONError(w, http.StatusForbidden, "missing permission: "+perm)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func PermissionsFromContext(ctx context.Context) ([]string, bool) {
	perms, ok := ctx.Value(permsCtxKey).([]string)
	return perms, ok
}

func DeploymentFromContext(ctx context.Context) (store.Deployment, bool) {
	dep, ok := ctx.Value(depCtxKey).(store.Deployment)
	return dep, ok
}