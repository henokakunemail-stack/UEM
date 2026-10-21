package usermgmt

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

type AuditLogger interface {
	Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error
}

type Handler struct {
	repo           *Repository
	sessions       SessionRevoker
	audit          AuditLogger
	authMiddleware func(http.Handler) http.Handler
}

// SessionRevoker is the slice of auth.SessionStore this module needs.
//
// It is an interface because auth imports nothing from here but this package
// should stay testable without a live session table, and because the type that
// matters is the behaviour ("withdraw every session a user holds"), not the
// concrete store. A nil SessionRevoker is legal and means "revoke nothing",
// which is what the tests that are not about sessions pass.
type SessionRevoker interface {
	RevokeAllForUser(ctx context.Context, userID string) error
}

func NewHandler(repo *Repository, sessions SessionRevoker, audit AuditLogger, authMiddleware func(http.Handler) http.Handler) *Handler {
	return &Handler{
		repo:           repo,
		sessions:       sessions,
		audit:          audit,
		authMiddleware: authMiddleware,
	}
}

// revokeSessions withdraws every session a user holds.
//
// This is the half of account control that does not happen by itself. Writing
// is_active = 0 stops the next login; it does not reach a console that is
// already signed in, because the access token it is holding names a row in
// auth_sessions that RequireAuth checks on every request -- so the row has to
// be revoked, not just the account flagged. Without this call, deactivating
// someone revokes their future but not their present, and a technician who was
// dismissed keeps admin-grade API access until their token expires on its own.
func (h *Handler) revokeSessions(r *http.Request, userID string) {
	if h.sessions == nil {
		return
	}
	if err := h.sessions.RevokeAllForUser(r.Context(), userID); err != nil {
		// The column is already written, so the account is closed to new logins
		// either way. Losing the revocation is still worth shouting about: it is
		// the difference between "signed out now" and "signed out in an hour".
		log.Error().Err(err).Str("user_id", userID).Msg("revoke all sessions failed")
	}
}

func (h *Handler) Register(r chi.Router) {
	// Self-service password change (any authenticated user)
	r.With(h.authMiddleware).Put("/api/users/me/password", h.changeSelfPassword)

	// Admin-only user management
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).Group(func(admin chi.Router) {
		admin.Get("/api/users", h.listUsers)
		admin.Post("/api/users", h.createUser)
		admin.Get("/api/users/{id}", h.getUser)
		admin.Put("/api/users/{id}", h.updateUser)
		admin.Put("/api/users/{id}/password", h.resetPassword)
		admin.Delete("/api/users/{id}", h.deactivateUser)
	})
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.repo.List(r.Context(), 100)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if users == nil {
		users = []User{}
	}
	writeJSON(w, http.StatusOK, users)
}

func (h *Handler) getUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	user, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "user not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	var req CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json payload")
		return
	}

	if req.Username == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "username and password are required")
		return
	}

	if len(req.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	if req.Role == "" {
		req.Role = rbac.RoleViewer
	}
	switch req.Role {
	case rbac.RoleAdmin, rbac.RoleTechnician, rbac.RoleViewer:
		// Valid roles
	default:
		writeErr(w, http.StatusBadRequest, "invalid role: use admin, technician, or viewer")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	now := time.Now().UTC()
	user := &User{
		ID:           NewID(),
		Username:     req.Username,
		PasswordHash: hash,
		Role:         req.Role,
		DisplayName:  req.DisplayName,
		IsActive:     true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := h.repo.Create(r.Context(), user); err != nil {
		if errors.Is(err, ErrDuplicate) {
			writeErr(w, http.StatusConflict, "username already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.audit.Log(r.Context(), "user", actorID, "user.create", user.ID, map[string]string{
		"username": user.Username,
		"role":     user.Role,
	})

	writeJSON(w, http.StatusCreated, user)
}

func (h *Handler) updateUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req UpdateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json payload")
		return
	}

	if req.Role != nil {
		switch *req.Role {
		case rbac.RoleAdmin, rbac.RoleTechnician, rbac.RoleViewer:
			// Valid
		default:
			writeErr(w, http.StatusBadRequest, "invalid role: use admin, technician, or viewer")
			return
		}
	}

	// An operator changing their own role, or turning their own account off,
	// is refused: either would sign them out of the console they are using.
	// Promoting yourself is not a change of role here (it already is admin),
	// and editing your own display name is allowed.
	actorID := auth.UserIDFromContext(r.Context())
	check := func(prior *User) error {
		if prior.ID != actorID {
			return nil
		}
		if req.Role != nil && *req.Role != prior.Role {
			return errOwnRole
		}
		if req.IsActive != nil && !*req.IsActive && prior.IsActive {
			return errDeactivateSelf
		}
		return nil
	}

	prior, updated, err := h.repo.UpdateGuarded(r.Context(), id, req, check)
	if err != nil {
		h.writeUpdateErr(w, err)
		return
	}

	_ = h.audit.Log(r.Context(), "user", actorID, "user.update", id, changedFields(prior, updated))

	// Sessions go only when they became wrong. The token carries the role, and
	// rbac.RequireRole reads the claim, not the row: a promoted viewer would
	// otherwise wait out an access token to become a technician, and a demoted
	// technician would keep terminal access for 15 minutes. A rename, or saving
	// the role it already had, changes nothing a token says.
	if prior.Role != updated.Role || (prior.IsActive && !updated.IsActive) {
		h.revokeSessions(r, id)
	}

	writeJSON(w, http.StatusOK, updated)
}

var (
	errOwnRole        = errors.New("cannot change your own role; ask another admin")
	errDeactivateSelf = errors.New("cannot deactivate your own account")
)

// writeUpdateErr maps the guarded update's refusals to a status and a code the
// console translates.
func (h *Handler) writeUpdateErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "user not found")
	case errors.Is(err, ErrNoFields):
		// Not a server fault: the payload carried nothing to change. 400 says
		// so, where 200 would claim an edit was saved that touched no column.
		writeErr(w, http.StatusBadRequest, "no fields to update: send role, display_name or is_active")
	case errors.Is(err, errOwnRole):
		writeErrCode(w, http.StatusBadRequest, "cannot_change_own_role", err.Error())
	case errors.Is(err, errDeactivateSelf):
		writeErrCode(w, http.StatusBadRequest, "cannot_deactivate_self", err.Error())
	case errors.Is(err, ErrLastAdmin):
		writeErrCode(w, http.StatusConflict, "last_admin", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

// changedFields is the audit detail for an edit: each field that changed,
// before and after. Separate keys rather than "a -> b" strings, because a
// display name is free text and a joined string could not be split back.
func changedFields(prior, updated *User) map[string]string {
	d := map[string]string{"username": updated.Username}
	if prior.Role != updated.Role {
		d["role_from"], d["role_to"] = prior.Role, updated.Role
	}
	if prior.IsActive != updated.IsActive {
		d["is_active_from"], d["is_active_to"] = strconv.FormatBool(prior.IsActive), strconv.FormatBool(updated.IsActive)
	}
	from, to := deref(prior.DisplayName), deref(updated.DisplayName)
	if from != to {
		d["display_name_from"], d["display_name_to"] = from, to
	}
	return d
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (h *Handler) resetPassword(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req ResetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json payload")
		return
	}

	if len(req.NewPassword) < 8 {
		writeErr(w, http.StatusBadRequest, "new password must be at least 8 characters")
		return
	}

	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	if err := h.repo.UpdatePassword(r.Context(), id, hash); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "user not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.audit.Log(r.Context(), "user", actorID, "user.reset_password", id, map[string]string{})

	// An admin resetting a password is usually doing it because the credential
	// leaked. Leaving the thief's session live would make the reset cosmetic:
	// the new password protects the next login, not the session already open.
	h.revokeSessions(r, id)

	writeJSON(w, http.StatusOK, map[string]string{"status": "password reset successfully"})
}

func (h *Handler) changeSelfPassword(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	if userID == "" {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req ChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json payload")
		return
	}

	if len(req.NewPassword) < 8 {
		writeErr(w, http.StatusBadRequest, "new password must be at least 8 characters")
		return
	}

	user, err := h.repo.GetByID(r.Context(), userID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}

	if err := auth.ComparePassword(user.PasswordHash, req.OldPassword); err != nil {
		writeErr(w, http.StatusBadRequest, "current password is incorrect")
		return
	}

	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	if err := h.repo.UpdatePassword(r.Context(), userID, hash); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = h.audit.Log(r.Context(), "user", userID, "user.change_password", userID, map[string]string{})

	// Deliberately no revokeSessions here, and the asymmetry with resetPassword
	// above is the point. An admin resetting somebody else's password is
	// usually reacting to a leak, so every session that user holds is suspect
	// and has to go. A user changing their own password has just proved they
	// know the current one; the likely reason is routine rotation or a shared
	// machine, and withdrawing the session they are making the request on would
	// sign them out mid-keystroke for no security gain. If that trade-off is
	// wrong for a given deployment it is a one-line change, not a redesign.

	writeJSON(w, http.StatusOK, map[string]string{"status": "password changed successfully"})
}

func (h *Handler) deactivateUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	actorID := auth.UserIDFromContext(r.Context())

	if id == actorID {
		writeErrCode(w, http.StatusBadRequest, "cannot_deactivate_self", errDeactivateSelf.Error())
		return
	}

	off := false
	prior, _, err := h.repo.UpdateGuarded(r.Context(), id, UpdateUserRequest{IsActive: &off}, nil)
	if err != nil {
		h.writeUpdateErr(w, err)
		return
	}

	_ = h.audit.Log(r.Context(), "user", actorID, "user.deactivate", id, map[string]string{"username": prior.Username})

	// Ordering matters and is not a detail: the flag is what stops the next
	// login, this is what ends the current one. Doing it the other way round
	// would leave a window where the sessions are gone but the account is still
	// open, which is a smaller version of the bug this closes.
	h.revokeSessions(r, id)

	writeJSON(w, http.StatusOK, map[string]string{"status": "user deactivated successfully"})
}

// writeErrCode adds a machine-readable code the console translates.
func writeErrCode(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
