// Command endpoint-mgmt-server is the central management server (Phase 1).
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jmoiron/sqlx"
	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/service"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/audit"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/cluster"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/config"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/dbmove"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/httpguard"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/logger"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/metrics"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/transport"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/wsticket"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/agentinstall"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/agentupdate"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/alerting"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/assetlicense"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/backup"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/dashboard"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/directory"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/maintenance"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/networkfilter"
	patchmgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/patch-management"
	remoteexec "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/remote-exec"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/remotecontrol"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/reports"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/securityaudit"
	softwaredeployment "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/software-deployment"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/systempolicy"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/taskscheduler"
	usermgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/user-management"
)

// serviceName is the OS service registration the server installer writes and
// the -service flag manages. It is deliberately distinct from the agent's
// "endpoint-agent": both run on the same Windows box during testing, and two
// services with one name is an install-time collision, not a shared config.
const serviceName = "endpoint-mgmt-server"

// serverVersion is stamped at build time (-ldflags "-X main.serverVersion=1.5.3")
// by the release script. It is reported by /healthz and recorded per node, so a
// mixed-version cluster mid-upgrade is visible instead of guessed at.
var serverVersion = "dev"

// dbPathForBackup resolves the database path for -backup and -restore.
//
// It reads only DB_PATH, and only after -env-file has been applied, because
// both flags run on a stopped server where the environment block may be empty.
// Routing this through config.Load would refuse to run without a JWT_SECRET.
func dbPathForBackup() string { return config.DBPathOrDefault() }

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
		backupPath    string
		restorePath   string
		moveToPG      bool
		moveFrom      string
		moveTo        string
		moveForce     bool
	)
	flag.StringVar(&httpAddr, "addr", "", "HTTP listen address override (default from HTTP_ADDR, else :8443)")
	flag.StringVar(&envFile, "env-file", "", "read KEY=VALUE runtime settings from a file (used by the service, which has no environment block)")
	flag.StringVar(&serviceAction, "service", "", "OS service management action (install|uninstall|start|stop|status)")
	flag.StringVar(&backupPath, "backup", "", "take a WAL-safe hot backup of the database to the specified path")
	flag.StringVar(&restorePath, "restore", "", "restore database from source file to active DB path")
	flag.BoolVar(&moveToPG, "migrate-to-postgres", false, "copy the SQLite database into PostgreSQL (with -from and -to)")
	flag.StringVar(&moveFrom, "from", "", "with -migrate-to-postgres: the SQLite file (default DB_PATH)")
	flag.StringVar(&moveTo, "to", "", "with -migrate-to-postgres: the PostgreSQL URL (default DB_URL)")
	flag.BoolVar(&moveForce, "force", false, "with -migrate-to-postgres: replace the contents of a PostgreSQL database that already holds data")
	flag.Parse()

	// A service launched by the SCM inherits nothing from the operator's
	// shell, so its configuration has to arrive by a path the SCM does carry.
	// This is read before anything that needs a database path -- including
	// -backup and -restore. Reading it after them meant a restore issued on a
	// service host resolved the relative default data/endpoint-mgmt.db, created
	// a fresh database there, and reported success against the wrong file.
	if envFile != "" {
		if err := loadEnvFile(envFile); err != nil {
			println("env-file error:", err.Error())
			os.Exit(1)
		}
	}

	if backupPath != "" || restorePath != "" {
		// Deliberately not config.Load(): backup and restore are offline
		// maintenance on a stopped server, and requiring JWT_SECRET to reach
		// the database path would block the one operation an operator has when
		// the server will not start.
		dbPath := dbPathForBackup()
		if backupPath != "" {
			if err := db.Backup(dbPath, backupPath); err != nil {
				println("backup error:", err.Error())
				os.Exit(1)
			}
			println("backup created successfully at:", backupPath)
			return
		}
		if err := db.Restore(restorePath, dbPath); err != nil {
			println("restore error:", err.Error())
			os.Exit(1)
		}
		println("database restored successfully from:", restorePath, "to:", dbPath)
		return
	}

	if moveToPG {
		os.Exit(runMoveToPostgres(moveFrom, moveTo, moveForce))
	}

	if serviceAction != "" {
		handleServiceAction(serviceAction, httpAddr, envFile)
		return
	}
	// The installer cannot invent a secret without shipping a CScript
	// dependency that does not exist on Linux, and baking one into a public
	// installer would be publishing it. So the file ships with the key
	// present and empty, and the first run fills it in.
	if envFile != "" {
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
// openDatabase connects using whichever backend the configuration selects.
//
// A configured DB_URL wins over DBDriver, and a configured DBDriver wins over
// the SQLite default -- so setting DB_DRIVER=postgres alone, with the URL in a
// variable the operator forgot, fails with a connection error naming the
// database rather than silently starting against an empty SQLite file.
func openDatabase(cfg config.Config) (*sqlx.DB, error) {
	if cfg.DBURL != "" {
		return db.OpenWithDriver(cfg.DBDriver, withStatementTimeout(cfg.DBURL, cfg.DBStatementTimeout))
	}
	if d := strings.ToLower(cfg.DBDriver); d != "" && d != "sqlite" {
		return nil, fmt.Errorf("DB_DRIVER=%s needs DB_URL to be set", cfg.DBDriver)
	}
	return db.Open(cfg.DBPath)
}

// withStatementTimeout adds DB_STATEMENT_TIMEOUT to a PostgreSQL URL as a
// session option, so a runaway query is cancelled by the database instead of
// holding a pool connection until the whole pool is exhausted. A URL that
// already sets statement_timeout, or a non-URL DSN, is left as written.
func withStatementTimeout(dsn string, timeout time.Duration) string {
	if timeout <= 0 || strings.Contains(dsn, "statement_timeout") ||
		(!strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://")) {
		return dsn
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	q := u.Query()
	opt := fmt.Sprintf("-c statement_timeout=%d", timeout.Milliseconds())
	if existing := q.Get("options"); existing != "" {
		opt = existing + " " + opt
	}
	q.Set("options", opt)
	u.RawQuery = q.Encode()
	return u.String()
}

// runMoveToPostgres is the -migrate-to-postgres command: an offline copy of
// the SQLite database into PostgreSQL. Like -backup it does not need the rest
// of the configuration, and it never writes to the SQLite file.
func runMoveToPostgres(from, to string, force bool) int {
	if from == "" {
		from = config.DBPathOrDefault()
	}
	if to == "" {
		to = os.Getenv("DB_URL")
	}
	if to == "" {
		fmt.Fprintln(os.Stderr, "migrate-to-postgres: pass -to postgres://user:pass@host:5432/db?sslmode=require (or set DB_URL)")
		return 2
	}
	fmt.Printf("Copying %s into PostgreSQL...\n", from)
	res, err := dbmove.Move(context.Background(), dbmove.Options{
		From: from, To: to, Force: force,
		Logf: func(format string, args ...any) { fmt.Printf(format+"\n", args...) },
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate-to-postgres failed:", err)
		return 1
	}
	total := 0
	for _, t := range res.Tables {
		total += t.Target
	}
	fmt.Printf("Done: %d tables, %d rows. Audit chain on PostgreSQL: %s.\n", len(res.Tables), total,
		map[bool]string{true: "verified", false: "NOT verified (it did not verify on SQLite either)"}[res.AuditChainOK])
	fmt.Println("Next: set DB_URL to the PostgreSQL URL in the server's env file and restart the server.")
	return 0
}

func run(cfg config.Config, stopService *context.CancelFunc) error {
	logger.InitWithRotation(cfg.LogLevel, cfg.LogFile, cfg.LogMaxMB, cfg.LogKeep)

	// Errors are returned, not log.Fatal'd: Fatal exits on the spot and skips
	// every deferred cleanup, and the caller already turns an error into a
	// non-zero exit.
	database, err := openDatabase(cfg)
	if err != nil {
		log.Error().Err(err).Str("driver", cfg.DBDriver).Msg("open database")
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()
	applyPoolSettings(database, cfg)

	// Phase 1 bootstrap: ensure an admin user exists so the console can log in.
	if err := bootstrapAdmin(database); err != nil {
		log.Error().Err(err).Msg("bootstrap admin")
		return fmt.Errorf("bootstrap admin: %w", err)
	}

	// A restore finishes by exiting; its outcome waits in a marker next to the
	// database. Record it in the audit trail of the database now live, and
	// turn a restore the process died in the middle of into a visible failure.
	if !db.IsPostgres(database) {
		if m, err := backup.FinalizeRestoreMarker(context.Background(), cfg.DBPath, &auditAdapter{db: database}); err != nil {
			log.Warn().Err(err).Msg("restore marker")
		} else if m != nil && m.State != backup.RestoreSucceeded && m.State != backup.RestorePending {
			log.Error().Str("restore_id", m.ID).Str("error_code", m.ErrorCode).Bool("rolled_back", m.RolledBack).
				Msg("the last database restore failed")
		}
	}

	srv, app := buildServer(cfg, database)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if stopService != nil {
		*stopService = cancel
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	var serveErr error
	select {
	case <-stop:
	case <-ctx.Done():
	case serveErr = <-app.serveErr:
		log.Error().Err(serveErr).Msg("http server stopped")
	}

	// Shutdown, in an order where each step can still rely on the next:
	//  1. readiness fails, and stays failing for ShutdownDrain, so a load
	//     balancer stops sending new requests before anything closes;
	//  2. the listener stops and in-flight requests finish;
	//  3. sockets the HTTP server does not own -- agents and relays -- close,
	//     so agents reconnect elsewhere instead of timing out against a dying
	//     process; their disconnect cleanup still has the database;
	//  4. background loops stop and in-flight webhooks finish;
	//  5. the database closes (deferred above), last.
	app.draining.Store(true)
	if serveErr == nil && cfg.ShutdownDrain > 0 {
		log.Info().Dur("drain", cfg.ShutdownDrain).Msg("shutting down: draining")
		time.Sleep(cfg.ShutdownDrain)
	}
	log.Info().Msg("shutting down")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("graceful shutdown")
	}
	app.teardown()
	log.Info().Msg("shutdown complete")
	return serveErr
}

// applyPoolSettings applies DB_MAX_OPEN, DB_MAX_IDLE and DB_CONN_LIFETIME over
// the driver defaults set in db.OpenWithDriver. Zero leaves a default alone.
func applyPoolSettings(d *sqlx.DB, cfg config.Config) {
	if cfg.DBMaxOpen > 0 {
		d.SetMaxOpenConns(cfg.DBMaxOpen)
	}
	if cfg.DBMaxIdle > 0 {
		d.SetMaxIdleConns(cfg.DBMaxIdle)
	}
	if cfg.DBConnLifetime > 0 {
		d.SetConnMaxLifetime(cfg.DBConnLifetime)
	}
}

// serverApp is what run needs from buildServer to shut down in order.
type serverApp struct {
	// draining makes /readyz answer 503 from the moment shutdown starts.
	draining atomic.Bool
	// serveErr receives the listener's error if it stops on its own -- a port
	// already in use, a bad certificate -- so run can shut down cleanly.
	serveErr chan error
	teardown func()
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
func buildServer(cfg config.Config, database *sqlx.DB) (*http.Server, *serverApp) {
	app := &serverApp{serveErr: make(chan error, 1)}
	started := time.Now()
	jwtSvc := auth.NewJWTService(cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL)
	deviceRepo := devicemgmt.NewRepository(database)
	hub := transport.NewHub()

	// This process's identity among the server nodes sharing the database. On
	// SQLite there is only ever this one, and the row is bookkeeping.
	node := cluster.NewNode(database, serverVersion)
	if err := node.Register(context.Background()); err != nil {
		log.Warn().Err(err).Msg("register server node")
	}

	// One session store, wired into RequireAuth and into login. jwtSvc is built
	// first because the rest of this function takes its middleware, and it is
	// handed the store afterwards; the store is still authoritative for every
	// authenticated route, because the middleware does not capture anything at
	// construction time and reads the store per request.
	//
	// This has to be the same store login writes to. A second store against the
	// same table is a second connection pool, but a second store against a
	// different database is a second source of truth, and revocation would
	// quietly stop applying to the tokens the middleware accepts.
	sessionStore := auth.NewSessionStore(database, cfg.RefreshTokenTTL)
	jwtSvc = jwtSvc.WithSessionStore(sessionStore)

	// On PostgreSQL several nodes can serve one fleet. The bus routes a command
	// to the node holding the agent's socket, and tells every node when a
	// session is revoked so none of them honours it from its cache.
	var bus *cluster.Bus
	if db.IsPostgres(database) {
		bus = cluster.NewBus(database, cfg.DBURL, node, hub, sessionStore)
		hub.SetRouter(bus)
		sessionStore.SetRevokeNotifier(bus.PublishSessionRevoked)
	}

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

	// One origin policy, built once from ALLOWED_ORIGIN_DOMAINS, shared by CORS
	// middleware and every WebSocket endpoint so the console is trusted consistently.
	checkOrigin := auth.NewOriginChecker(cfg.AllowedOriginDomains)

	r := chi.NewRouter()

	// Resilience and request correlation middlewares.
	r.Use(middleware.RequestID)
	// The client address, taken from X-Forwarded-For only behind a proxy listed
	// in TRUSTED_PROXIES (chi's RealIP trusted the header from anyone).
	ipResolver := auth.NewClientIPResolver(cfg.TrustedProxies)
	r.Use(ipResolver.Middleware)
	// Metrics and the access log, outside Recoverer so a panic is still
	// counted and logged as the 500 it became.
	reg := metrics.NewRegistry()
	r.Use(metrics.NewHTTP(reg).Middleware)
	r.Use(middleware.Recoverer)

	// Cross-Origin Resource Sharing (CORS) support for REST API.
	r.Use(httpguard.CORS(httpguard.CORSOptions{
		OriginValidator:  checkOrigin,
		AllowCredentials: true,
		MaxAge:           86400,
	}))

	// Security headers on all responses.
	// CSP: default-src 'self' allows same-origin scripts/styles/fonts;
	// style-src 'unsafe-inline' needed for inline styles in the embedded SPA.
	r.Use(httpguard.SecurityHeaders(httpguard.DefaultSecurityHeaders(cfg.TLSCertFile != "" && cfg.TLSKeyFile != "")))

	// Token-bucket API rate limiter (300 req/min per client IP, burst 100).
	// Bypasses the persistent agent WebSocket endpoint /api/agent/connect and health probes.
	apiLimiter := httpguard.NewAPIRateLimiter(300, 100)
	r.Use(apiLimiter.Middleware(func(req *http.Request) string {
		if req.URL.Path == "/api/agent/connect" || req.URL.Path == "/healthz" || req.URL.Path == "/readyz" || req.URL.Path == "/metrics" {
			return ""
		}
		return ipResolver.ClientIP(req)
	}))

	// leader is assigned once the background loops are wired below; the
	// handlers read it per request.
	var leader *cluster.Leader
	isLeader := func() bool { return leader != nil && leader.IsLeader() }

	// /readyz is what a load balancer routes on. It fails while the node is
	// draining for shutdown, and when the database is unreachable -- a node
	// that cannot reach the database cannot serve anything. The cluster bus
	// being down is reported but not failed on: the node still serves its own
	// agents and every request that does not need another node.
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{"node": node.ID, "agents_online": hub.Count(), "leader": isLeader()}
		if bus != nil {
			body["cluster_bus"] = bus.Connected()
		}
		if app.draining.Load() {
			body["status"] = "draining"
			writeJSON(w, http.StatusServiceUnavailable, body)
			return
		}
		if err := database.PingContext(r.Context()); err != nil {
			body["status"] = "degraded"
			body["error"] = "database unreachable: " + err.Error()
			writeJSON(w, http.StatusServiceUnavailable, body)
			return
		}
		body["status"] = "ready"
		body["database"] = "ok"
		writeJSON(w, http.StatusOK, body)
	})

	// /healthz is liveness: the process is up and answering. It says which
	// node and version answered, which is the first thing to know when two
	// nodes disagree.
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":         "ok",
			"version":        serverVersion,
			"node":           node.ID,
			"uptime_seconds": int(time.Since(started).Seconds()),
			"agents_online":  hub.Count(),
			"leader":         isLeader(),
		})
	})

	// /metrics, in the Prometheus text format. With METRICS_TOKEN set it
	// requires that bearer token; compared in constant time so the token
	// cannot be recovered by timing.
	r.Get("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if cfg.MetricsToken != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(cfg.MetricsToken)) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_ = reg.Write(w)
	})
	reg.GaugeFunc("em_build_info", "Server build and node identity.",
		map[string]string{"version": serverVersion, "node": node.ID}, func() float64 { return 1 })
	reg.GaugeFunc("em_uptime_seconds", "Seconds since this node started.", nil,
		func() float64 { return time.Since(started).Seconds() })
	reg.GaugeFunc("em_agents_connected", "Agent sockets held by this node.", nil,
		func() float64 { return float64(hub.Count()) })
	reg.GaugeFunc("em_leader", "1 when this node runs the leader-only background loops.", nil,
		func() float64 {
			if isLeader() {
				return 1
			}
			return 0
		})
	reg.GaugeFunc("em_db_open_connections", "Open database connections.", nil,
		func() float64 { return float64(database.Stats().OpenConnections) })
	reg.GaugeFunc("em_db_in_use_connections", "Database connections in use.", nil,
		func() float64 { return float64(database.Stats().InUse) })
	reg.GaugeFunc("em_db_wait_count", "Requests that waited for a database connection, total.", nil,
		func() float64 { return float64(database.Stats().WaitCount) })
	if bus != nil {
		reg.GaugeFunc("em_cluster_commands_forwarded", "Commands sent to an agent on another node, total.", nil,
			func() float64 { return float64(bus.Forwarded.Load()) })
		reg.GaugeFunc("em_cluster_commands_delivered", "Commands received from another node and delivered, total.", nil,
			func() float64 { return float64(bus.Delivered.Load()) })
		reg.GaugeFunc("em_cluster_bus_connected", "1 when the cluster bus listener is connected.", nil,
			func() float64 {
				if bus.Connected() {
					return 1
				}
				return 0
			})
	}

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
	loginH.WithSessionStore(sessionStore)
	loginH.WithTrustedProxies(cfg.TrustedProxies)
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
	enrollH.WithTrustedProxies(cfg.TrustedProxies)
	enrollH.Register(r)

	// Phase 5: remote command execution and interactive terminal relay.
	remoteExecRepo := remoteexec.NewRepository(database)
	termRelay := remoteexec.NewTerminalRelay()
	remoteExecH := remoteexec.NewHandler(remoteExecRepo, hub, termRelay, &auditAdapter{db: database}, jwtSvc.RequireAuth, deviceRepo, checkOrigin)

	wsH := transport.NewWSHandler(hub, deviceRepo, database, cfg.AgentOfflineAfter).
		WithNode(node.ID).
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
	softH := softwaredeployment.NewHandler(softRepo, hub, &auditAdapter{db: database}, filepath.Join(cfg.DataDir, "packages"), jwtSvc.RequireAuth, deviceRepo)
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
	go node.Heartbeat(bgCtx)
	if bus != nil {
		go func() {
			if err := bus.Run(bgCtx); err != nil {
				log.Error().Err(err).Msg("cluster bus stopped; commands for agents on other nodes are not delivered")
			}
		}()
	}

	// leaderOnly collects the loops that must run on exactly one server node.
	// Each one either acts on shared rows (the scheduler fires scripts, the
	// evaluator opens incidents and sends webhooks) or would only duplicate work
	// (the sweeps, the session purge). They start once this node is elected and
	// stop if it loses leadership; on SQLite the only node is always leader.
	var leaderOnly []func(context.Context)

	// Expired session rows are reaped on a timer rather than at login. They are
	// only deleted after expiry, so the interval is a cleanliness knob, not a
	// correctness one: a late purge leaves the table a little larger and changes
	// nothing about which token works, and IsLive already refuses anything past
	// expires_at.
	//
	// Hourly is not a tuned value. The table grows by one row per login plus one
	// per refresh, and every one of those rows is beyond its TTL when it goes;
	// anything from hourly to daily is fine, and hourly keeps a fleet that
	// refreshes constantly from accumulating a week's worth of dead rows between
	// sweeps.
	leaderOnly = append(leaderOnly, func(ctx context.Context) { go purgeSessions(ctx, sessionStore, time.Hour) })

	// Phase 9: alerting & notification engine.
	alertRepo := alerting.NewRepository(database)
	alertEval := alerting.NewEvaluator(database, alertRepo).WithAuditor(&auditAdapter{db: database})
	alertH := alerting.NewHandler(alertRepo, alertEval, &auditAdapter{db: database}, jwtSvc.RequireAuth)
	alertH.Register(r)
	leaderOnly = append(leaderOnly, func(ctx context.Context) { alertEval.StartBackgroundEvaluator(ctx, 30*time.Second) })

	// Phase 10: task scheduler & script repository.
	schedRepo := taskscheduler.NewRepository(database)
	schedulerSvc := taskscheduler.NewScheduler(schedRepo, hub)
	schedH := taskscheduler.NewHandler(schedRepo, schedulerSvc, &auditAdapter{db: database}, jwtSvc.RequireAuth, deviceRepo)
	schedH.Register(r)
	leaderOnly = append(leaderOnly, func(ctx context.Context) { schedulerSvc.StartBackgroundScheduler(ctx, 30*time.Second) })

	// Scheduled-task device runs stranded by an agent that vanished mid-script.
	// SyncRunStatus only runs from the agent result handler, so a device run
	// that never reports leaves its parent at 'running' forever.
	leaderOnly = append(leaderOnly, func(ctx context.Context) {
		go schedRepo.StartSweep(ctx, taskscheduler.SweepInterval, taskscheduler.AbandonGrace)
	})

	// Phase 11: remote control & screen capture relay.
	rcRepo := remotecontrol.NewRepository(database)
	// Relays are in memory only, so a session still 'active' at start was left
	// by a process that exited without closing it. Ended here, before any
	// route is served, so history never shows it as live.
	if n, err := rcRepo.EndStaleSessions(bgCtx, node.ID, time.Now().UTC().Add(-cluster.NodeAliveWithin)); err != nil {
		log.Warn().Err(err).Msg("end stale remote control sessions")
	} else if n > 0 {
		log.Info().Int64("sessions", n).Msg("ended remote control sessions left active by a previous run")
	}
	rcRelay := remotecontrol.NewRelayManager(rcRepo)
	rcH := remotecontrol.NewHandler(rcRepo, rcRelay, hub, deviceRepo, &auditAdapter{db: database}, jwtSvc.RequireAuth, checkOrigin)
	rcH.Register(r)

	// Phase 12: network & web filter security policies.
	filterRepo := networkfilter.NewRepository(database)
	filterH := networkfilter.NewHandler(filterRepo, hub, deviceRepo, &auditAdapter{db: database}, jwtSvc.RequireAuth)
	filterH.Register(r)

	// Phase 13: agent self-update & rollout management.
	updateRepo := agentupdate.NewRepository(database)
	updateH := agentupdate.NewHandler(updateRepo, hub, deviceRepo, &auditAdapter{db: database},
		filepath.Join(cfg.DataDir, "agent-releases"), jwtSvc.RequireAuth, cfg.UpdateMinimumVersion).
		WithSigningPublicKey(cfg.UpdateSigningPublicKey, cfg.UpdateMinimumVersion)
	updateH.Register(r)

	// One-line Linux agent installer: the script, and the binary handed out
	// against a valid enrollment token.
	// The release package carries agents/ next to the server binary, which is
	// where the update scripts put it; DATA_DIR/agents is for a hand-placed copy.
	bundleDir := cfg.AgentBundleDir
	if bundleDir == "" {
		bundleDir = filepath.Join(cfg.DataDir, "agents")
	}
	exeAgents := ""
	if exe, err := os.Executable(); err == nil {
		exeAgents = filepath.Join(filepath.Dir(exe), "agents")
	}
	// The same bundle is what the console registers as releases, so an
	// enrolled fleet can be moved onto the agents a server update brought.
	updateH.WithBundledAgents(serverVersion, bundleDir, exeAgents)
	agentinstall.NewHandler(deviceRepo, func(ctx context.Context, osName, arch string) (string, error) {
		rel, err := updateRepo.LatestActiveRelease(ctx, osName, arch)
		if err != nil {
			return "", err
		}
		return rel.FilePath, nil
	}, bundleDir, exeAgents).Register(r)

	// Queued updates are delivered when the device reconnects, not when the
	// operator presses Dispatch again. Wired here rather than at construction
	// because the update handler is built below the agent websocket handler.
	wsH.WithUpdateQueue(updateH)
	// Software tasks: refusals come back under the task id, and tasks queued
	// for an offline device are sent after its next hello.
	wsH.WithCommandResultReceiver(softH).WithReconnectQueue(softH)
	// Patch install jobs are sent under the job id; a refusal fails the job.
	wsH.WithCommandResultReceiver(patchH)
	// Same for the filter policy: a sync clicked against a device that was offline
	// wrote status='pending' and nothing ever re-read the row, so the endpoint
	// stayed unfiltered until someone pressed Sync a second time.
	wsH.WithFilterSync(filterH)

	// Endpoint system policies (Active Directory GPO-like management).
	sysPolicyRepo := systempolicy.NewRepository(database).WithVersionKey(cfg.JWTSecret)
	// Earlier versions stored the managed admin password in every device's
	// state row; clear those once.
	sysPolicyRepo.ScrubStoredStatePasswords(context.Background())
	sysPolicyH := systempolicy.NewHandler(sysPolicyRepo, hub, deviceRepo, &auditAdapter{db: database}, jwtSvc.RequireAuth).
		WithDataDir(cfg.DataDir)
	sysPolicyH.Register(r)
	wsH.WithSystemPolicySync(sysPolicyH)
	// Group membership changes re-dispatch both policy kinds that can target a
	// group; the inventory module only knows the interface.
	invH.WithMembershipObservers(filterH, sysPolicyH)

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

	// Phase 17: backup & restore management.
	backupH := backup.NewHandler(cfg, database, &auditAdapter{db: database}, jwtSvc.RequireAuth)
	backupH.Register(r)

	// Automated 40-point pre-launch security posture auditor.
	secAuditH := securityaudit.NewHandler(&cfg, database, &auditAdapter{db: database}, jwtSvc.RequireAuth).
		WithSetupPassword(os.Getenv("ADMIN_PASSWORD"))
	secAuditH.Register(r)

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
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
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
	leaderOnly = append(leaderOnly, func(ctx context.Context) { go runDeploymentSweep(ctx, softRepo, softH) })

	// Maintenance tasks stranded by an agent that took the command and then
	// never reported. Nothing else closes them: the job status is written only
	// from a task's terminal report, so one silent task holds its job 'running'
	// and the console polls it forever.
	leaderOnly = append(leaderOnly, func(ctx context.Context) {
		go maintRepo.StartSweep(ctx, maintenance.SweepInterval, maintenance.AbandonGrace)
	})

	// Agent update tasks stranded on offline devices or dead agents. Without
	// this, offline endpoints leave update campaigns stuck in 'in_progress' forever.
	leaderOnly = append(leaderOnly, func(ctx context.Context) {
		updateRepo.StartSweep(ctx, agentupdate.SweepInterval, agentupdate.TaskTimeoutGrace)
	})
	// Patch install jobs with no result for 30 minutes are failed as no_report.
	leaderOnly = append(leaderOnly, func(ctx context.Context) { patchRepo.StartSweep(ctx, patchmgmt.SweepInterval) })
	// Enrollment rows that never checked in are retired a week after their
	// token expired, so they stop counting as offline devices.
	leaderOnly = append(leaderOnly, func(ctx context.Context) { go invH.RunPendingSweep(ctx, time.Hour) })
	// Campaign waves are a schedule in the database, released by whichever
	// node leads; the same loop retries queued updates for online devices.
	leaderOnly = append(leaderOnly, func(ctx context.Context) { go updateH.RunWaves(ctx, agentupdate.WaveInterval) })

	leader = cluster.NewLeader(database)
	go leader.Run(bgCtx, func(ctx context.Context) {
		for _, start := range leaderOnly {
			start(ctx)
		}
	})

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
	stopBackups := func() {}
	if strings.ToLower(cfg.DBDriver) != "postgres" && strings.ToLower(cfg.DBDriver) != "postgresql" && !strings.HasPrefix(cfg.DBURL, "postgres://") && !strings.HasPrefix(cfg.DBURL, "postgresql://") {
		var backupCtx context.Context
		backupCtx, stopBackups = context.WithCancel(context.Background())
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
	} else {
		log.Info().Msg("online database backup job skipped on PostgreSQL (managed via external pg_dump)")
	}

	// Listen and block until the process is asked to stop.
	go func() {
		if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
			log.Info().Str("addr", cfg.HTTPAddr).
				Str("cert", cfg.TLSCertFile).Str("key", cfg.TLSKeyFile).
				Msg("server listening (TLS)")
			if err := srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
				app.serveErr <- fmt.Errorf("https server: %w", err)
			}
		} else {
			log.Warn().Str("addr", cfg.HTTPAddr).
				Msg("server listening (PLAINTEXT — set TLS_CERT_FILE and TLS_KEY_FILE for production)")
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				app.serveErr <- fmt.Errorf("http server: %w", err)
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
	//
	// The order matters. Agent sockets and relays close first, while the
	// database is still open for their disconnect cleanup; the node then leaves
	// the cluster so no other node routes to it; the heartbeat flusher writes
	// its last batch; the background loops stop; and webhooks already sent get
	// a moment to finish.
	reg.GaugeFunc("em_relays_active", "Live relays on this node.", map[string]string{"kind": "remote_control"},
		func() float64 { return float64(rcRelay.ActiveCount()) })
	reg.GaugeFunc("em_relays_active", "Live relays on this node.", map[string]string{"kind": "terminal"},
		func() float64 { return float64(termRelay.ActiveCount()) })

	cleanup := func() {
		hub.Close()
		rcRelay.CloseAll()
		termRelay.CloseAll()
		node.Leave(context.Background())
		wsH.Close()
		loginH.Close()
		apiLimiter.Close()
		stopBackups()
		stopBackground()
		alertEval.WaitWebhooks(5 * time.Second)
	}
	app.teardown = cleanup
	return srv, app
}

// purgeSessions deletes auth_sessions rows past their TTL, on a timer, for the
// life of the process. It logs failures rather than exiting: the loop's own
// health is visible through the error itself, and stopping a cleanup loop on a
// transient SQLITE_BUSY would turn one slow write into no cleanup at all until
// the next restart.
func purgeSessions(ctx context.Context, store *auth.SessionStore, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := store.PurgeExpired(ctx); err != nil {
				log.Error().Err(err).Msg("purge expired auth sessions")
			}
		}
	}
}

// bootstrapAdmin creates the default admin if no users exist yet.
// The password comes from ADMIN_PASSWORD env var; if unset, a random 24-char
// password is generated and printed once to the server log.
func bootstrapAdmin(d *sqlx.DB) error {
	if pass := os.Getenv("ADMIN_PASSWORD"); pass != "" {
		hash, err := auth.HashPassword(pass)
		if err == nil {
			_, _ = d.Exec(`UPDATE users SET password_hash = ? WHERE username = 'admin'`, hash)
		}
	}
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
func runDeploymentSweep(ctx context.Context, repo *softwaredeployment.Repository, h *softwaredeployment.Handler) {
	ticker := time.NewTicker(softwaredeployment.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			// Queued tasks for devices that are online are retried first: a
			// send can fail on a busy socket, and a device that stays
			// connected never reconnects to trigger the flush.
			h.ResendQueued(sweepCtx)
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
