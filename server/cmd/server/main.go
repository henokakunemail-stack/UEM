// Command endpoint-mgmt-server is the central management server (Phase 1).
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/service"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/audit"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/config"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/logger"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/transport"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/wsticket"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/agentupdate"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/alerting"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/assetlicense"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/dashboard"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/directory"
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

// serviceName is the OS service registration the server installer writes and
// the -service flag manages. It is deliberately distinct from the agent's
// "endpoint-agent": both run on the same Windows box during testing, and two
// services with one name is an install-time collision, not a shared config.
const serviceName = "endpoint-mgmt-server"

func main() {
	// A management server that has to be started by hand after every reboot is
	// not a management server, so it runs under the same OS service abstraction
	// the agent uses. The flag is parsed before config.Load because a -service
	// action is a request *about* the installation, and must work on a machine
	// whose config is not yet complete.
	var (
		httpAddr      string
		envFile       string
		serviceAction string
	)
	flag.StringVar(&httpAddr, "addr", "", "HTTP listen address override (default from HTTP_ADDR, else :8443)")
	flag.StringVar(&envFile, "env-file", "", "read KEY=VALUE runtime settings from a file (used by the service, which has no environment block)")
	flag.StringVar(&serviceAction, "service", "", "OS service management action (install|uninstall|start|stop|status)")
	flag.Parse()

	if serviceAction != "" {
		handleServiceAction(serviceAction, httpAddr, envFile)
		return
	}
	// A service launched by the SCM inherits nothing from the operator's
	// shell, so its configuration has to arrive by a path the SCM does carry.
	if envFile != "" {
		if err := loadEnvFile(envFile); err != nil {
			println("env-file error:", err.Error())
			os.Exit(1)
		}
		// The installer cannot invent a secret without shipping a CScript
		// dependency that does not exist on Linux, and baking one into a public
		// installer would be publishing it. So the file ships with the key
		// present and empty, and the first run fills it in.
		if err := ensureJWTSecret(envFile); err != nil {
			println("secret error:", err.Error())
			os.Exit(1)
		}
	}

	// svc.Run is the only thing that keeps a Windows service transition to
	// RUNNING, so the whole server has to live inside it. Interactive runs skip
	// it and fall through to the ordinary signal handler below.
	var stopServer context.CancelFunc
	serve := func() error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if httpAddr != "" {
			cfg.HTTPAddr = httpAddr
		}
		return run(cfg, &stopServer)
	}
	if service.RunAsService() {
		if err := service.Serve(serviceName, func() error {
			return serve()
		}); err != nil {
			println("service error:", err.Error())
			os.Exit(1)
		}
		return
	}
	if err := serve(); err != nil {
		println("startup error:", err.Error())
		os.Exit(1)
	}
}

// run boots the database, the router and the background workers, then blocks
// until a stop is signalled or the service handler cancels it.
func run(cfg config.Config, stopService *context.CancelFunc) error {
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if stopService != nil {
		*stopService = cancel
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case <-stop:
	case <-ctx.Done():
	}
	log.Info().Msg("shutting down")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("graceful shutdown")
	}
	return nil
}

// ensureJWTSecret gives the server a signing secret on first run when the env
// file does not carry one, and writes it back so a later restart -- including
// one performed by the OS rather than by the operator -- keeps the same secret
// and does not invalidate every issued token.
//
// The rewrite goes through a temp file and a rename so that a crash mid-write
// cannot leave a truncated secret, which would be worse than none: it would
// start, sign tokens, and then be unable to verify any of them on restart.
func ensureJWTSecret(path string) error {
	if os.Getenv("JWT_SECRET") != "" {
		return nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("generate signing secret: %w", err)
	}
	secret := hex.EncodeToString(b)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	replaced := false
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "JWT_SECRET=") {
			lines[i] = "JWT_SECRET=" + secret
			replaced = true
			break
		}
	}
	if !replaced {
		lines = append(lines, "JWT_SECRET="+secret)
	}
	body := strings.Join(lines, "\n")

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	// A service on Windows runs as LocalSystem, which can usually not read a
	// file it did not create. Broadening here costs nothing: the file already
	// holds a secret the service must read, and Administrators is who manages
	// the install.
	_ = os.Chmod(path, 0o600)
	return os.Setenv("JWT_SECRET", secret)
}

// loadEnvFile applies KEY=VALUE lines to the process environment. Values already
// present in the environment win, so an operator who starts the server by hand
// with a one-off override is not silently overruled by the file on disk.
func loadEnvFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("line %d is not KEY=VALUE: %q", i+1, line)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set %s: %w", key, err)
		}
	}
	return nil
}

// handleServiceAction implements -service. The arguments the service is
// registered with are exactly the ones the installer's env file sets, so a
// service started later reads the same configuration as a console run.
func handleServiceAction(action, httpAddr, envFile string) {
	args := []string{}
	if httpAddr != "" {
		args = append(args, "-addr", httpAddr)
	}
	if envFile != "" {
		args = append(args, "-env-file", envFile)
	}

	cfg := service.Config{
		Name:        serviceName,
		DisplayName: "Enterprise Endpoint Management Server",
		Description: "Central endpoint management server: fleet, console, and agent gateway.",
		Arguments:   args,
	}
	mgr, err := service.NewManager(cfg)
	if err != nil {
		println("service error:", err.Error())
		os.Exit(1)
	}

	switch action {
	case "install":
		if err := mgr.Install(); err != nil {
			println("install service failed:", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Service '%s' installed.\n", cfg.Name)
	case "uninstall", "remove":
		if err := mgr.Uninstall(); err != nil {
			println("uninstall service failed:", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Service '%s' removed.\n", cfg.Name)
	case "start":
		if err := mgr.Start(); err != nil {
			println("start service failed:", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Service '%s' started.\n", cfg.Name)
	case "stop":
		if err := mgr.Stop(); err != nil {
			println("stop service failed:", err.Error())
			os.Exit(1)
		}
		fmt.Printf("Service '%s' stopped.\n", cfg.Name)
	case "status":
		status, err := mgr.Status()
		if err != nil {
			fmt.Printf("Service '%s' status: %s (%v)\n", cfg.Name, status, err)
		} else {
			fmt.Printf("Service '%s' status: %s\n", cfg.Name, status)
		}
	default:
		fmt.Printf("unknown -service action %q (install|uninstall|start|stop|status)\n", action)
		os.Exit(1)
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
	// One store, shared. It is the same table the login handler writes, the
	// WebSocket ticket issuer reads, and user-management revokes against, so a
	// second instance would be a second source of truth about the same sessions
	// rather than a second connection.
	sessionStore := auth.NewSessionStore(database, cfg.RefreshTokenTTL)

	loginH := auth.NewLoginHandler(database, jwtSvc)
	loginH.WithSessionStore(sessionStore)
	loginH.Register(r)

	// WebSocket handshakes cannot carry an Authorization header, so the console
	// trades its access token for a single-use ticket over an ordinary request.
	// Wired here, before any listener is serving, because the two handshake
	// handlers are built without a database handle and read this from the
	// package rather than from their own fields.
	auth.SetWebSocketTicketIssuer(
		wsticket.NewStore(database, wsticket.DefaultMaxLive),
		sessionStore,
	)

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
	userH := usermgmt.NewHandler(userRepo, sessionStore, &auditAdapter{db: database}, jwtSvc.RequireAuth)
	userH.Register(r)

	// Phase 8: reports & export engine.
	reportsRepo := reports.NewRepository(database)
	reportsH := reports.NewHandler(reportsRepo, jwtSvc.RequireAuth)
	reportsH.Register(r)

	// Background loops that run for the life of the process. The cancel is owned
	// by the cleanup closure at the end of this function; see the note there.
	bgCtx, stopBackground := context.WithCancel(context.Background())

	// Phase 9: alerting & notification engine.
	alertRepo := alerting.NewRepository(database)
	alertEval := alerting.NewEvaluator(database, alertRepo)
	alertH := alerting.NewHandler(alertRepo, alertEval, &auditAdapter{db: database}, jwtSvc.RequireAuth)
	alertH.Register(r)
	alertEval.StartBackgroundEvaluator(bgCtx, 30*time.Second)

	// Phase 10: task scheduler & script repository.
	schedRepo := taskscheduler.NewRepository(database)
	schedulerSvc := taskscheduler.NewScheduler(schedRepo, hub)
	schedH := taskscheduler.NewHandler(schedRepo, schedulerSvc, &auditAdapter{db: database}, jwtSvc.RequireAuth, deviceRepo)
	schedH.Register(r)
	schedulerSvc.StartBackgroundScheduler(bgCtx, 30*time.Second)

	// Scheduled-task device runs stranded by an agent that vanished mid-script.
	// SyncRunStatus only runs from the agent result handler, so a device run
	// that never reports leaves its parent at 'running' forever.
	go schedRepo.StartSweep(bgCtx, taskscheduler.SweepInterval, taskscheduler.AbandonGrace)

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

	// Queued updates are delivered when the device reconnects, not when the
	// operator presses Dispatch again. Wired here rather than at construction
	// because the update handler is built below the agent websocket handler.
	wsH.WithUpdateQueue(updateH)
	// Same for the filter policy: a sync clicked against a device that was offline
	// wrote status='pending' and nothing ever re-read the row, so the endpoint
	// stayed unfiltered until someone pressed Sync a second time.
	wsH.WithFilterSync(filterH)

	// Phase 14: asset & license management.
	assetRepo := assetlicense.NewRepository(database)
	assetH := assetlicense.NewHandler(assetRepo, &auditAdapter{db: database}, jwtSvc.RequireAuth)
	assetH.Register(r)

	// Phase 15: device maintenance.
	maintH.Register(r)

	// Phase 16: LDAP / Active Directory directory sync. Contacts only — the
	// synced people are not console accounts and no auth code is touched.
	dirH := directory.NewHandler(
		directory.NewRepository(database),
		&auditAdapter{db: database},
		directory.NewLDAPClient(),
		cfg.LDAPBindPassword,
		jwtSvc.RequireAuth,
	)
	dirH.Register(r)

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
	go runOfflineSweep(bgCtx, database, hub, cfg.AgentOfflineAfter)

	// Deployment tasks whose agent died mid-install never get a final report:
	// the agent posts progress over HTTP from a goroutine, and a logout, an
	// agent update, or a pulled power cord all end that goroutine silently. This
	// reaps them so a rollout does not read as 'still running' forever.
	go runDeploymentSweep(bgCtx, softRepo)

	// Maintenance tasks stranded by an agent that took the command and then
	// never reported. Nothing else closes them: the job status is written only
	// from a task's terminal report, so one silent task holds its job 'running'
	// and the console polls it forever.
	go maintRepo.StartSweep(bgCtx, maintenance.SweepInterval, maintenance.AbandonGrace)

	// Periodic online database backups. VACUUM INTO takes a consistent
	// snapshot without stopping the server, so a failed backup is logged but
	// never fatal.
	//
	// The cancel is owned by the cleanup closure below, not deferred here. A
	// defer in this function fires when buildServer returns -- at startup, a few
	// microseconds after the goroutine below is launched -- and StartBackupJob's
	// first act is a 30-second settle delay that select's against ctx.Done(). The
	// cancel always wins that race, so the job returned before taking a single
	// snapshot, every boot, on the default configuration. Meanwhile the log line
	// underneath printed "scheduled online database backups" with the directory
	// and interval, so the safety net was reported as armed and was dead.
	backupCtx, stopBackups := context.WithCancel(context.Background())
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

	// Cleanup runs at process shutdown, from run(), not at the end of this
	// function. A defer here would fire the moment the router is wired -- at
	// startup, before the first request -- and both of these own a background
	// loop that has to keep running for the server's whole life:
	//
	//   - wsH.Close stops the heartbeat flusher. handleHeartbeat keeps appending
	//     to its pending map, which nothing drains any more, so last_seen_at is
	//     never written again and the map grows by one entry per device for the
	//     life of the process.
	//   - loginH.Close stops the login rate limiter's prune ticker, so every
	//     source IP that ever failed a login stays resident forever.
	//
	//   - stopBackground stops the alert evaluator and the task scheduler
	//     poller. Both were started with their own context.Background() and a
	//     bare `for range ticker.C`, so neither could be stopped. While the API
	//     kept answering 200 the alert rules kept being evaluated and the due
	//     interval schedules kept being dispatched, in a process that was on its
	//     way down.
	//
	// Both are silent: the API keeps answering 200 while the thing that makes
	// those answers correct quietly stops happening.
	cleanup := func() {
		wsH.Close()
		loginH.Close()
		stopBackups()
		stopBackground()
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
func runOfflineSweep(ctx context.Context, d *sqlx.DB, hub *transport.Hub, threshold time.Duration) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// The staleness cutoff is part of the one SELECT, deliberately. Filtering
			// per device instead would be 10,000 extra round trips every 15 seconds
			// on a large fleet, which is the cost the batched write below exists to
			// avoid.
			//
			// This also had a correctness bug: the sweep used to flip every
			// socket-less device offline regardless of age, so a device that was
			// merely mid-reconnect was marked down, and AGENT_OFFLINE_AFTER only
			// ever set the read deadline.
			//
			// A device with a NULL last_seen_at has never reported, so there is no
			// staleness to judge; enrollment decides its initial state, and guessing
			// here would flip a freshly enrolled device offline on the first tick.
			now := time.Now().UTC()
			var ids []string
			err := d.Select(&ids,
				`SELECT id FROM devices
			 WHERE status = 'online'
			   AND last_seen_at IS NOT NULL
			   AND last_seen_at < ?`,
				now.Add(-threshold))
			if err != nil {
				log.Debug().Err(err).Msg("offline sweep: list stale devices")
				continue
			}

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
func runDeploymentSweep(ctx context.Context, repo *softwaredeployment.Repository) {
	ticker := time.NewTicker(softwaredeployment.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			n, err := repo.AbandonOrphanedTasks(sweepCtx, softwaredeployment.AbandonGrace)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Warn().Err(err).Msg("deployment sweep: reap orphaned tasks")
				continue
			}
			if n > 0 {
				log.Warn().Int64("tasks", n).
					Msg("deployment sweep: marked tasks failed because their agent stopped reporting")
			}
		}
	}
}

// markOfflineBatch flips a batch of devices to offline inside a single
// transaction, so the cost is one fsync per sweep rather than one per device.
func markOfflineBatch(d *sqlx.DB, ids []string, now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
