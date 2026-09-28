package rbac

import (
	"context"
	"encoding/json"
	"net/http"
)

// deny writes the same {"error": ...} shape every handler in this server uses.
// http.Error would emit text/plain, and the console's request() helper parses
// JSON — so an RBAC refusal surfaced as "Request failed with HTTP 403" with no
// explanation of which role would have been accepted.
func deny(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// Role levels. Higher = more privileged.
const (
	RoleViewer     = "viewer"
	RoleTechnician = "technician"
	RoleAdmin      = "admin"
)

var rank = map[string]int{
	RoleViewer:     1,
	RoleTechnician: 2,
	RoleAdmin:      3,
}

// RequireRole returns middleware allowing roles with rank >= minRole.
// It must be chained after auth.RequireAuth so the role is in the context.
func RequireRole(minRole string) func(http.Handler) http.Handler {
	minRank, ok := rank[minRole]
	if !ok {
		panic("rbac: unknown role " + minRole)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role, _ := r.Context().Value(roleKey{}).(string)
			if role == "" {
				deny(w, http.StatusForbidden, "unauthorized: no role in context")
				return
			}
			got, ok := rank[role]
			if !ok || got < minRank {
				deny(w, http.StatusForbidden, "forbidden: role '"+role+"' insufficient, requires '"+minRole+"' or higher")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type roleKey struct{}

// WithRole stores the role in context. Used by auth middleware adapters.
func WithRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, roleKey{}, role)
}
