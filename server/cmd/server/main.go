// Command endpoint-mgmt-server is the central management server (Phase 1).
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/audit"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/config"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/logger"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/transport"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/agentupdate"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/alerting"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/assetlicense"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/dashboard"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/maintenance"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/networkfilter"
	patchmgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/patch-management"
	remoteexec "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/remote-exec"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/remotecontrol"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/reports"
	softwaredeployment "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/software-deployment"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/taskscheduler"
	usermgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/user-management"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		// Logger not initialized yet; write to stderr directly.
		println("config error:", err.Error())
		os.Exit(1)
	}
	logger.Init(cfg.LogLevel, cfg.LogFile)

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatal().Err(err).Str("db", cfg.DBPath).Msg("open database")
	}
	defer database.Close()

	// Phase 1 bootstrap: ensure an admin user exists so the console can log in.
	if err := bootstrapAdmin(database); err != nil {
		log.Fatal().Err(err).Msg("bootstrap admin")
	}

	srv, stopBackground := buildServer(cfg, database)
	defer stopBackground()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Info().Msg("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("graceful shutdown")
	}
}

// buildServer wires every module's routes onto one router and starts the
// background workers the server depends on. It is split out of main() so tests
// can build the exact production router and assert that every path the web
// console calls is actually registered — a route typo in either side then
// fails the build instead of silently 404-ing in the browser.
//
// The returned cleanup stops the background goroutines; main() passes nil.
func buildServer(cfg config.Config, database *sqlx.DB) (*http.Server, func()) {
	jwtSvc := auth.NewJWTService(cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL)
	deviceRepo := devicemgmt.NewRepository(database)
	hub := transport.NewHub()

	// Phase 2: inventory receiver + HTTP routes. The handler needs the hub both to
	// answer "is this device online" and to deliver the collect request; the
	// routes need the real JWT middleware, injected here to avoid a package-level
	// dependency from the module on the auth service internals.
	invRepo := devicemgmt.NewInventoryRepository(database)
	invH := devicemgmt.NewInventoryHandler(invRepo, database, hub).
		WithAuth(jwtSvc.RequireAuth)

	// Phase 15: device maintenance. The handler is both the console's REST
	// surface and the agent's step-report receiver, so it is constructed here,
	// above wsH, and wired into the socket handler below.
	maintRepo := maintenance.NewRepository(database)
	maintH := maintenance.NewHandler(maintRepo, hub, &auditAdapter{db: database}, jwtSvc.RequireAuth, deviceRepo)

	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "agents_online": hub.Count()})
	})

	// Server log tail, backing the console's "Log" menu. The audit trail
	// (/api/audit-logs) records operator actions; this records what the server
	// itself did, which is what you actually read when a menu 500s.
	r.With(jwtSvc.RequireAuth, rbac.RequireRole(rbac.RoleViewer)).
		Get("/api/logs", func(w http.ResponseWriter, r *http.Request) {
			lines := logger.Tail()
			// Default to the last 200 lines: the console is a viewer, not a
			// log archive — the file on disk is the archive.
			limit := 200
			if v := r.URL.Query().Get("limit"); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					limit = n
				}
			}
			if limit > len(lines) {
				limit = len(lines)
			}
			if lines == nil {
				lines = []string{}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"lines":     lines[len(lines)-limit:],
				"total":     len(lines),
				"log_file":  cfg.LogFile,
				"truncated": limit < len(lines),
			})
		})

	// Admin console auth (public endpoints).
	loginH := auth.NewLoginHandler(database, jwtSvc)
	defer loginH.Close()
	loginH.Register(r)

	// Agent endpoints (authenticated by per-device secret, not JWT).
	enrollH := devicemgmt.NewEnrollmentHandler(deviceRepo, database)
	enrollH.Register(r)

	// One origin policy, built once from ALLOWED_ORIGIN_DOMAINS, shared by every
	// WebSocket endpoint so the console is trusted consistently.
	checkOrigin := auth.NewOriginChecker(cfg.AllowedOriginDomains)

	// Phase 5: remote command execution and interactive terminal relay.
	remoteExecRepo := remoteexec.NewRepository(database)
	termRelay := remoteexec.NewTerminalRelay()
	remoteExecH := remoteexec.NewHandler(remoteExecRepo, hub, termRelay, &auditAdapter{db: database}, jwtSvc, jwtSvc.RequireAuth, deviceRepo, checkOrigin)

	wsH := transport.NewWSHandler(hub, deviceRepo, database, cfg.AgentOfflineAfter).
		WithInventory(invH).
		WithTerminal(termRelay).
		WithOriginChecker(checkOrigin)
	defer wsH.Close()

	r.Handle("/api/agent/connect", wsH)

	// Device management API (JWT + RBAC).
	deviceH := devicemgmt.NewHandler(deviceRepo, database, jwtSvc, cfg.EnrollmentTTL)
	deviceH.Register(r)

	// Phase 2: inventory, device lifecycle and static groups.
	invH.Register(r)

	// Phase 3: dashboard executive metrics.
	dashRepo := dashboard.NewRepository(database)
	dashH := dashboard.NewHandler(dashRepo, jwtSvc.RequireAuth)
	dashH.Register(r)

	// Phase 4: software package repository and deployment engine.
	softRepo := softwaredeployment.NewRepository(database)
	softH := softwaredeployment.NewHandler(softRepo, hub, &auditAdapter{db: database}, "./data/packages", jwtSvc.RequireAuth, deviceRepo)
	softH.Register(r)

	// Phase 5: remote execution & terminal routes.
	remoteExecH.Register(r)

	// Phase 6: patch management & OS updates.
	patchRepo := patchmgmt.NewRepository(database)
	patchH := patchmgmt.NewHandler(patchRepo, hub, &auditAdapter{db: database}, jwtSvc, jwtSvc.RequireAuth, deviceRepo)
	patchH.Register(r)

	// Phase 7: user management & lifecycle.
	userRepo := usermgmt.NewRepository(database)
	userH := usermgmt.NewHandler(userRepo, &auditAdapter{db: database}, jwtSvc.RequireAuth)
	userH.Register(r)

	// Phase 8: reports & export engine.
	reportsRepo := reports.NewRepository(database)
	reportsH := reports.NewHandler(reportsRepo, jwtSvc.RequireAuth)
	reportsH.Register(r)

	// Phase 9: alerting & notification engine.
	alertRepo := alerting.NewRepository(database)
	alertEval := alerting.NewEvaluator(database, alertRepo)
	alertH := alerting.NewHandler(alertRepo, alertEval, &auditAdapter{db: database}, jwtSvc.RequireAuth)
	alertH.Register(r)
	alertEval.StartBackgroundEvaluator(30 * time.Second)

	// Phase 10: task scheduler & script repository.
	schedRepo := taskscheduler.NewRepository(database)
	schedulerSvc := taskscheduler.NewScheduler(schedRepo, hub)
	schedH := taskscheduler.NewHandler(schedRepo, schedulerSvc, &auditAdapter{db: database}, jwtSvc.RequireAuth, deviceRepo)
	schedH.Register(r)
	schedulerSvc.StartBackgroundScheduler(30 * time.Second)

	// Phase 11: remote control & screen capture relay.
	rcRepo := remotecontrol.NewRepository(database)
	rcRelay := remotecontrol.NewRelayManager(rcRepo)
	rcH := remotecontrol.NewHandler(rcRepo, rcRelay, hub, deviceRepo, &auditAdapter{db: database}, jwtSvc, jwtSvc.RequireAuth, checkOrigin)
	rcH.Register(r)

	// Phase 12: network & web filter security policies.
	filterRepo := networkfilter.NewRepository(database)
	filterH := networkfilter.NewHandler(filterRepo, hub, deviceRepo, &auditAdapter{db: database}, jwtSvc.RequireAuth)
	filterH.Register(r)

	// Phase 13: agent self-update & rollout management.
	updateRepo := agentupdate.NewRepository(database)
	updateH := agentupdate.NewHandler(updateRepo, hub, deviceRepo, &auditAdapter{db: database}, "./data/agent-releases", jwtSvc.RequireAuth)
	updateH.Register(r)

	// Phase 14: asset & license management.
	assetRepo := assetlicense.NewRepository(database)
	assetH := assetlicense.NewHandler(assetRepo, &auditAdapter{db: database}, jwtSvc.RequireAuth)
	assetH.Register(r)

	// Phase 15: device maintenance.
	maintH.Register(r)

	// Command dispatch demo endpoint: send "ping" to a device's live connection.
	r.With(jwtSvc.RequireAuth, rbac.RequireRole(rbac.RoleTechnician)).
		Post("/api/devices/{id}/ping", func(w http.ResponseWriter, r *http.Request) {
			deviceID := chi.URLParam(r, "id")
			// Confirm the device exists before queueing anything.
			if _, err := deviceRepo.GetByID(r.Context(), deviceID); err != nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not found"})
				return
			}
			// Persist the command first, so the agent's later reply has a row to
			// update even if the connection drops in between.
			cmdID := devicemgmt.NewID()
			now := time.Now().UTC()
			if _, err := database.ExecContext(r.Context(), `
				INSERT INTO agent_commands (id, device_id, command_type, payload, status, created_at, sent_at)
				VALUES (?, ?, 'ping', '{}', 'sent', ?, ?)`,
				cmdID, deviceID, now, now); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			// If the agent is not connected the command stays queued as 'sent'
			// rather than being lost — it can be redelivered on reconnect.
			if !hub.Online(deviceID) {
				writeJSON(w, http.StatusOK, map[string]string{"status": "queued", "command_id": cmdID})
				return
			}
			if !hub.SendTo(deviceID, mustJSON(transport.Envelope{
				Type:    transport.TypeCommand,
				ID:      cmdID,
				Command: "ping",
			})) {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "device connection is busy"})
				return
			}
			_ = audit.Log(r.Context(), database, "user",
				auth.UserIDFromContext(r.Context()), "command.send", deviceID,
				map[string]string{"command_type": "ping", "command_id": cmdID})
			writeJSON(w, http.StatusOK, map[string]string{"status": "sent", "command_id": cmdID})
		})

	// Mount embedded Web Console SPA on all non-API routes.
	registerWebConsole(r)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	// Background sweeper: mark devices whose agents went silent as offline.
	go runOfflineSweep(database, hub, cfg.AgentOfflineAfter)

	// Deployment tasks whose agent died mid-install never get a final report:
	// the agent posts progress over HTTP from a goroutine, and a logout, an
	// agent update, or a pulled power cord all end that goroutine silently. This
	// reaps them so a rollout does not read as 'still running' forever.
	go runDeploymentSweep(softRepo)

	// Periodic online database backups. VACUUM INTO takes a consistent
	// snapshot without stopping the server, so a failed backup is logged but
	// never fatal.
	backupCtx, stopBackups := context.WithCancel(context.Background())
	defer stopBackups()
	go db.StartBackupJob(backupCtx, database, db.BackupConfig{
		Dir:      cfg.BackupDir,
		Interval: cfg.BackupInterval,
		Retain:   cfg.BackupRetain,
		Prefix:   "endpoint-mgmt",
	}, func(err error) {
		log.Error().Err(err).Msg("database backup failed")
	})
	log.Info().Str("dir", cfg.BackupDir).
		Dur("interval", cfg.BackupInterval).
		Int("retain", cfg.BackupRetain).
		Msg("scheduled online database backups")

	// Listen and block until the process is asked to stop.
	go func() {
		if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
			log.Info().Str("addr", cfg.HTTPAddr).
				Str("cert", cfg.TLSCertFile).Str("key", cfg.TLSKeyFile).
				Msg("server listening (TLS)")
			if err := srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatal().Err(err).Msg("https server")
			}
		} else {
			log.Warn().Str("addr", cfg.HTTPAddr).
				Msg("server listening (PLAINTEXT — set TLS_CERT_FILE and TLS_KEY_FILE for production)")
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatal().Err(err).Msg("http server")
			}
		}
	}()

	cleanup := func() {
		stopBackups()
	}
	return srv, cleanup
}

// bootstrapAdmin creates the default admin if no users exist yet.
// The password comes from ADMIN_PASSWORD env var; if unset, a random 24-char
// password is generated and printed once to the server log.
func bootstrapAdmin(d *sqlx.DB) error {
	var count int
	if err := d.Get(&count, `SELECT COUNT(*) FROM users`); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	password := os.Getenv("ADMIN_PASSWORD")
	generated := false
	if password == "" {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		password = hex.EncodeToString(b)
		generated = true
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = d.Exec(`INSERT INTO users (id, username, password_hash, role, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		devicemgmt.NewID(), "admin", hash, rbac.RoleAdmin, time.Now().UTC(), time.Now().UTC())
	if err != nil {
		return err
	}
	if generated {
		log.Info().Str("username", "admin").Str("password", password).
			Msg("bootstrap: created default admin with GENERATED password (CHANGE IT NOW)")
	} else {
		log.Info().Str("username", "admin").
			Msg("bootstrap: created default admin with password from ADMIN_PASSWORD env")
	}
	return nil
}

// runOfflineSweep periodically flags devices as offline when their agents have
// not been heard from within the threshold. The hub is authoritative for liveness:
// a device with a live socket is never marked offline here even during a
// transient heartbeat stall, and a device whose socket is gone is marked offline
// by the disconnect handler itself. This sweeper catches the remaining case —
// a socket that died silently (e.g. NAT timeout) without a close frame.
func runOfflineSweep(d *sqlx.DB, hub *transport.Hub, threshold time.Duration) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		var ids []string
		err := d.Select(&ids, `SELECT id FROM devices WHERE status = 'online'`)
		if err != nil {
			log.Debug().Err(err).Msg("offline sweep: list online devices")
			continue
		}
		now := time.Now().UTC()

		// Collect first, then write in ONE transaction. A per-device UPDATE
		// would mean 10,000 separate write transactions every sweep on a large
		// fleet, reintroducing exactly the SQLite write-lock contention the
		// HeartbeatFlusher exists to avoid.
		stale := make([]string, 0, len(ids))
		for _, id := range ids {
			if hub.Online(id) {
				continue // live socket — trust it over last_seen
			}
			stale = append(stale, id)
		}
		if len(stale) == 0 {
			continue
		}
		markOfflineBatch(d, stale, now)
	}
}

// runDeploymentSweep closes out deployment tasks whose agent vanished.
//
// A software task reports progress by POSTing to the server from a goroutine in
// the agent. Nothing guarantees that goroutine finishes: the agent can be
// killed, restarted by the update engine mid-install, or lose power. Without
// this the task row stays in 'installing' forever, its parent deployment never
// completes, and the console shows a rollout in progress for a machine that was
// switched off an hour ago.
//
// The sweep runs on the same cadence as the offline sweep and after it, so a
// device is marked offline first and its tasks are reaped on a later tick.
func runDeploymentSweep(repo *softwaredeployment.Repository) {
	ticker := time.NewTicker(softwaredeployment.SweepInterval)
	defer ticker.Stop()
	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		n, err := repo.AbandonOrphanedTasks(ctx, softwaredeployment.AbandonGrace)
		cancel()
		if err != nil {
			log.Warn().Err(err).Msg("deployment sweep: reap orphaned tasks")
			continue
		}
		if n > 0 {
			log.Warn().Int64("tasks", n).
				Msg("deployment sweep: marked tasks failed because their agent stopped reporting")
		}
	}
}

// markOfflineBatch flips a batch of devices to offline inside a single
// transaction, so the cost is one fsync per sweep rather than one per device.
func markOfflineBatch(d *sqlx.DB, ids []string, now time.Time) {	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tx, err := d.BeginTxx(ctx, nil)
	if err != nil {
		log.Debug().Err(err).Msg("offline sweep: begin batch")
		return
	}
	defer tx.Rollback()

	stmt, err := tx.PreparexContext(ctx,
		`UPDATE devices SET status = 'offline', updated_at = ? WHERE id = ? AND status = 'online'`)
	if err != nil {
		log.Debug().Err(err).Msg("offline sweep: prepare batch")
		return
	}
	for _, id := range ids {
		if _, err := stmt.ExecContext(ctx, now, id); err != nil {
			log.Debug().Err(err).Str("device", id).Msg("offline sweep: mark")
		}
	}
	if err := stmt.Close(); err != nil {
		log.Debug().Err(err).Msg("offline sweep: close stmt")
		return
	}
	if err := tx.Commit(); err != nil {
		log.Debug().Err(err).Msg("offline sweep: commit batch")
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"type":"error"}`)
	}
	return b
}

type auditAdapter struct {
	db *sqlx.DB
}

func (a *auditAdapter) Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error {
	return audit.Log(ctx, a.db, actorType, actorID, action, targetID, details)
}
