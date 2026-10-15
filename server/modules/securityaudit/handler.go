package securityaudit

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/config"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

type Auditor interface {
	Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error
}

type Handler struct {
	cfg       *config.Config
	db        *sqlx.DB
	auditor   Auditor
	authMw    func(http.Handler) http.Handler
	evaluator *Evaluator

	mu           sync.RWMutex
	cachedReport *AuditReport
}

func NewHandler(cfg *config.Config, db *sqlx.DB, auditor Auditor, authMw func(http.Handler) http.Handler) *Handler {
	return &Handler{
		cfg:       cfg,
		db:        db,
		auditor:   auditor,
		authMw:    authMw,
		evaluator: NewEvaluator(cfg, db),
	}
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(cr chi.Router) {
		cr.Use(h.authMw)
		cr.Use(rbac.RequireRole(rbac.RoleAdmin))

		cr.Get("/api/system/security-audit", h.getSecurityAudit)
		cr.Post("/api/system/security-audit/run", h.runSecurityAudit)
	})
}

func (h *Handler) getSecurityAudit(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	cached := h.cachedReport
	h.mu.RUnlock()

	// If no cached report or older than 5 minutes, generate fresh report
	if cached == nil || time.Since(cached.GeneratedAt) > 5*time.Minute {
		report := h.evaluator.Evaluate(r.Context())
		h.mu.Lock()
		h.cachedReport = &report
		h.mu.Unlock()
		cached = &report
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(cached)
}

func (h *Handler) runSecurityAudit(w http.ResponseWriter, r *http.Request) {
	report := h.evaluator.Evaluate(r.Context())

	h.mu.Lock()
	h.cachedReport = &report
	h.mu.Unlock()

	if h.auditor != nil {
		actorID := auth.UserIDFromContext(r.Context())
		_ = h.auditor.Log(r.Context(), "user", actorID, "security.audit_run", "system", map[string]string{
			"score":  string(rune(report.Score)),
			"grade":  report.Grade,
			"passed": string(rune(report.Passed)),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(report)
}
