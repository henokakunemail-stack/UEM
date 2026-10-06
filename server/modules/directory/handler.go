package directory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

type Auditor interface {
	Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error
}

type Handler struct {
	repo    *Repository
	auditor Auditor
	client  directoryClient
	authMw  func(http.Handler) http.Handler
	// bindPassword comes from the environment, never from the database, and is
	// copied onto the Config at dial time rather than stored on it.
	bindPassword string
}

func NewHandler(repo *Repository, auditor Auditor, client directoryClient, bindPassword string, authMw func(http.Handler) http.Handler) *Handler {
	return &Handler{
		repo:         repo,
		auditor:      auditor,
		client:       client,
		bindPassword: bindPassword,
		authMw:       authMw,
	}
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(cr chi.Router) {
		cr.Use(h.authMw)

		// Contacts are read by a technician, not just an admin: the hardware
		// asset form is RoleTechnician and its PIC dropdown needs this. A name,
		// an email and a department are not a credential.
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/directory/contacts", h.handleListContacts)

		// Everything below names the corporate directory or its credentials.
		cr.With(rbac.RequireRole(rbac.RoleAdmin)).Get("/api/directory/config", h.handleGetConfig)
		cr.With(rbac.RequireRole(rbac.RoleAdmin)).Put("/api/directory/config", h.handleUpdateConfig)
		cr.With(rbac.RequireRole(rbac.RoleAdmin)).Post("/api/directory/sync/preview", h.handlePreview)
		cr.With(rbac.RequireRole(rbac.RoleAdmin)).Post("/api/directory/sync/apply", h.handleApply)
		cr.With(rbac.RequireRole(rbac.RoleAdmin)).Post("/api/directory/test", h.handleTest)
	})
}

func (h *Handler) handleListContacts(w http.ResponseWriter, r *http.Request) {
	list, err := h.repo.ListContacts(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, found, err := h.repo.GetConfig(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		// Not an error and not a 404: nothing has been configured yet, which is
		// the normal first-run state. Returning the defaults with empty strings
		// lets the Settings form render straight away instead of handling a
		// missing-config case it would only ever hit once.
		writeJSON(w, http.StatusOK, ConfigView{
			Port:            636,
			UseTLS:          true,
			SearchFilter:    "(objectClass=person)",
			Source:          "ldap",
			BindPasswordSet: h.bindPassword != "",
		})
		return
	}
	writeJSON(w, http.StatusOK, viewOf(cfg, h.bindPassword != ""))
}

func (h *Handler) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var in Config
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, errBadJSON)
		return
	}
	in.BindPassword = "" // never taken from the request body
	if strings.TrimSpace(in.Host) == "" {
		writeErr(w, http.StatusBadRequest, errNoHost)
		return
	}
	// A port of 0 would silently become "pick one for me" inside the client,
	// so the bound is here where the message can name the field.
	if in.Port < 1 || in.Port > 65535 {
		writeErr(w, http.StatusBadRequest, errBadPort)
		return
	}
	if strings.TrimSpace(in.BindDN) == "" {
		writeErr(w, http.StatusBadRequest, errNoBindDN)
		return
	}

	in.UpdatedBy = auth.UserIDFromContext(r.Context())
	if err := h.repo.UpsertConfig(r.Context(), &in); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.auditor.Log(r.Context(), "user", actorID, "directory.config.update", "", map[string]string{
		"host": in.Host,
		"base": in.BaseDN,
		"bind": in.BindDN,
		"tls":  boolStr(in.UseTLS),
	})

	writeJSON(w, http.StatusOK, viewOf(&in, h.bindPassword != ""))
}

// liveConfig reads the saved config and attaches the env-sourced password.
// Every sync path goes through it, so there is one place that knows the secret
// and it is not the database.
func (h *Handler) liveConfig(r *http.Request) (*Config, error) {
	cfg, found, err := h.repo.GetConfig(r.Context())
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errNotConfigured
	}
	cfg.BindPassword = h.bindPassword
	return cfg, nil
}

// planFor is the whole of a sync: read the directory, read what is stored, and
// diff them. It writes nothing.
//
// Both preview and apply call it, and apply calls it again rather than reusing
// what a preview produced. That is the point: a plan stored from five minutes
// ago was computed against a database that has since changed, and acting on it
// would make the console's preview a claim about a past rather than about now.
func (h *Handler) planFor(r *http.Request) (*SyncPlan, error) {
	cfg, err := h.liveConfig(r)
	if err != nil {
		return nil, err
	}
	incoming, err := h.client.Search(r.Context(), *cfg)
	if err != nil {
		return nil, err
	}
	existing, err := h.repo.AllContacts(r.Context())
	if err != nil {
		return nil, err
	}
	plan := diffContacts(existing, incoming)
	return &plan, nil
}

func (h *Handler) handlePreview(w http.ResponseWriter, r *http.Request) {
	plan, err := h.planFor(r)
	if err != nil {
		h.writePlanErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (h *Handler) handleApply(w http.ResponseWriter, r *http.Request) {
	plan, err := h.planFor(r)
	if err != nil {
		h.writePlanErr(w, err)
		return
	}
	if plan.Empty() {
		// Still a 200 with the empty plan rather than an error: "nothing to do"
		// is the successful outcome of a sync that has already converged, and
		// the console shows it as such.
		writeJSON(w, http.StatusOK, plan)
		return
	}
	if err := h.repo.ApplyPlan(r.Context(), *plan); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.auditor.Log(r.Context(), "user", actorID, "directory.sync.apply", "", map[string]string{
		"adds":          itoa(len(plan.Adds)),
		"updates":       itoa(len(plan.Updates)),
		"deactivations": itoa(len(plan.Deactivations)),
		"in_directory":  itoa(plan.TotalInDirectory),
	})

	writeJSON(w, http.StatusOK, plan)
}

func (h *Handler) handleTest(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.liveConfig(r)
	if err != nil {
		h.writePlanErr(w, err)
		return
	}
	n, err := h.client.Test(r.Context(), *cfg)
	if err != nil {
		// A failed connection test is a valid answer, not a server fault: the
		// console's request() throws on any non-2xx, so a 502 here made the
		// ok:false branch unreachable and the UI could only reach this verdict
		// through the generic error path. 200 keeps ok=false the same shape as
		// the success case, so the page renders both from one code path.
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":           false,
			"message":      err.Error(),
			"entries_seen": 0,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"message":      "Bind succeeded.",
		"entries_seen": n,
	})
}

// writePlanErr distinguishes "you have not configured a directory" from "the
// directory said no", because only one of them is fixable by filling in the
// form on this page.
func (h *Handler) writePlanErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotConfigured) {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// A refused bind or an unreachable host is the caller's input being wrong,
	// not a fault in this server. 502 says that without pretending to know more.
	writeErr(w, http.StatusBadGateway, err)
}

func viewOf(c *Config, passwordSet bool) ConfigView {
	return ConfigView{
		Host:            c.Host,
		Port:            c.Port,
		UseTLS:          c.UseTLS,
		BaseDN:          c.BaseDN,
		BindDN:          c.BindDN,
		SearchFilter:    c.SearchFilter,
		Source:          c.Source,
		BindPasswordSet: passwordSet,
		UpdatedBy:       c.UpdatedBy,
		UpdatedAt:       c.UpdatedAt,
	}
}

var (
	errNotConfigured = &appError{"directory sync is not configured yet — save a host and base DN first"}
	errBadJSON       = &appError{"invalid json"}
	errNoHost        = &appError{"host is required"}
	errBadPort       = &appError{"port must be between 1 and 65535"}
	errNoBindDN      = &appError{"bind_dn is required — use a read-only service account, not your own login"}
)

// appError is an error with a message already fit to show an operator. Anything
// that is not one of these is a fault and is reported as its own text, which
// is why writeErr can hand err.Error() to the console without a second lookup.
type appError struct{ msg string }

func (e *appError) Error() string { return e.msg }

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func itoa(n int) string { return strconv.Itoa(n) }
