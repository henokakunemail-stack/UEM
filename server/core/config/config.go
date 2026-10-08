package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the server runtime configuration. Loaded once at startup.
type Config struct {
	HTTPAddr string // HTTP listen address, e.g. ":8443"
	// DBDriver selects the database: "sqlite" (the default, and the single-binary
	// standalone mode) or "postgres". Left empty it is inferred from DBURL.
	DBDriver string
	// DBURL is a PostgreSQL connection string. Empty means SQLite, using DBPath.
	DBURL           string
	DBPath          string // SQLite database file path
	JWTSecret       string // secret used to sign JWTs
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration

	// TLS: optional cert/key paths. When both are set, the server uses
	// ListenAndServeTLS; otherwise it falls back to plain HTTP and logs a
	// warning. In production, either set these or terminate TLS at the reverse
	// proxy (nginx/Caddy).
	TLSCertFile string
	TLSKeyFile  string

	// Enrollment token TTL and agent offline threshold.
	EnrollmentTTL     time.Duration
	AgentOfflineAfter time.Duration // a device is offline if last_seen older than this

	// Periodic online database backups (SQLite VACUUM INTO).
	BackupDir      string
	BackupInterval time.Duration
	BackupRetain   int

	// AllowedOriginDomains lists extra browser origins permitted to open a
	// WebSocket to this server, beyond loopback and same-host. Entries may be
	// exact ("console.example.com") or wildcard ("*.example.com" matches any
	// single-or-deeper subdomain, but never the bare apex). Comma-separated in
	// ALLOWED_ORIGIN_DOMAINS. Empty means "same-host only", which is the safe
	// default: it never silently trusts a domain nobody configured.
	AllowedOriginDomains []string

	LogLevel string // zerolog level: debug|info|warn|error

	// LogFile is an optional path for a full server log. When empty, logs go to
	// stdout only. The console's "Log" menu tails this file, so on a systemd
	// box the operator can point it at a real file and still read it in the UI.
	LogFile string
	// LogTailLines caps how many recent lines GET /api/logs holds in memory.
	LogTailLines int

	// LDAPBindPassword authenticates the directory bind account, and it is an
	// env var rather than a database column on purpose: this server has no
	// encryption at rest and no other stored credential, so a bind password in
	// SQLite would mean any copy of the .db is a copy of the company
	// directory. The console only ever learns whether one is set.
	//
	// Trade-off: rotating it needs a server restart.
	LDAPBindPassword string

	// TrustedProxies are the peer addresses allowed to set X-Forwarded-For and
	// X-Real-IP. ClientIP honours those headers only when the connection came
	// from one of these; from anybody else it reads RemoteAddr, which the peer
	// cannot forge.
	//
	// Empty means loopback only, which is the default that stays safe on a bare
	// deployment. Behind nginx or Caddy the operator lists the proxy's address
	// here, and an attacker who can set the header is then still capped by
	// their own address. Comma-separated in TRUSTED_PROXIES, and each entry may
	// be a plain IP ("10.0.0.4") or a CIDR ("10.0.0.0/8"), so a whole internal
	// subnet can be named without enumerating load-balancer replicas.
	TrustedProxies []string

	// UpdateSigningPublicKey is the base64 Ed25519 public key whose signature
	// an agent demands on a release manifest before it will install it.
	//
	// Only the public half lives here. The private key stays on the signing
	// host -- the CI job or the offline box that produced the signature -- and
	// never on the server that serves the artifact, which is the whole point of
	// signing: a server that is fully compromised cannot ship a binary of its
	// own choosing, because it cannot make a signature the fleet was built to
	// accept.
	//
	// Empty means the server cannot attest a signature, and the sign endpoint
	// refuses rather than recording one it has no way to check. Agents are
	// unaffected: they trust the key they were built with, not this value.
	UpdateSigningPublicKey string

	// UpdateMinimumVersion is the fleet-wide floor on agent versions. A release
	// may declare its own minimum on top of this, and the stricter of the two
	// applies. It exists for the "every supported version is at least X"
	// decision that is not a property of any one release -- for example when a
	// version has to be banned fleet-wide and the releases below it are not
	// being re-signed. Agents that were built with a trusted key enforce it;
	// /api/agent/config publishes it to operators and tooling.
	UpdateMinimumVersion string
}

// DBPathOrDefault returns the configured database path without requiring the
// rest of the configuration to be valid.
//
// Load rejects a missing JWT_SECRET, and backup/restore are exactly the
// operations an operator runs on a stopped server -- often a server that will
// not start in the first place. Routing them through Load would make the
// recovery path depend on the thing that is broken.
func DBPathOrDefault() string {
	return getEnv("DB_PATH", "data/endpoint-mgmt.db")
}

// Load reads configuration from environment variables with sane defaults.
func Load() (Config, error) {
	dbPath := DBPathOrDefault()
	cfg := Config{
		HTTPAddr:             getEnv("HTTP_ADDR", ":8443"),
		DBDriver:             getEnv("DB_DRIVER", ""),
		DBURL:                getEnv("DB_URL", ""),
		DBPath:               dbPath,
		JWTSecret:            getEnv("JWT_SECRET", ""),
		LDAPBindPassword:     getEnv("LDAP_BIND_PASSWORD", ""),
		AccessTokenTTL:       getDuration("ACCESS_TOKEN_TTL", 15*time.Minute),
		RefreshTokenTTL:      getDuration("REFRESH_TOKEN_TTL", 24*7*time.Hour),
		TLSCertFile:          getEnv("TLS_CERT_FILE", ""),
		TLSKeyFile:           getEnv("TLS_KEY_FILE", ""),
		EnrollmentTTL:        getDuration("ENROLLMENT_TTL", 30*time.Minute),
		AgentOfflineAfter:    getDuration("AGENT_OFFLINE_AFTER", 90*time.Second),
		BackupDir:            getEnv("BACKUP_DIR", defaultBackupDir(dbPath)),
		BackupInterval:       getDuration("BACKUP_INTERVAL", time.Hour),
		BackupRetain:         getEnvInt("BACKUP_RETAIN", 24),
		AllowedOriginDomains: getCSVEnv("ALLOWED_ORIGIN_DOMAINS"),
		TrustedProxies:       getCSVEnv("TRUSTED_PROXIES"),
		// Read verbatim rather than trimmed: a whitespace typo in a base64 key is
		// a misconfiguration an operator wants reported, and trimming it here
		// would turn "the wrong key is recorded" into "a key is recorded and
		// nothing verifies against it", which is far harder to diagnose.
		UpdateSigningPublicKey: os.Getenv("UPDATE_SIGNING_PUBLIC_KEY"),
		UpdateMinimumVersion:   strings.TrimSpace(getEnv("UPDATE_MINIMUM_VERSION", "")),
		LogLevel:               getEnv("LOG_LEVEL", "info"),
		LogFile:                getEnv("LOG_FILE", ""),
		LogTailLines:           getEnvInt("LOG_TAIL_LINES", 2000),
	}
	if cfg.JWTSecret == "" {
		return cfg, fmt.Errorf("JWT_SECRET must be set (generate one, e.g. 32+ random bytes)")
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// defaultBackupDir places snapshots in a "backups" folder beside the database.
func defaultBackupDir(dbPath string) string {
	dir := "."
	if filepath.Dir(dbPath) != "" {
		dir = filepath.Dir(dbPath)
	}
	return filepath.Join(dir, "backups")
}

func getEnvInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// getCSVEnv reads a comma-separated environment variable into a trimmed,
// non-empty slice. An unset or empty variable yields nil, which callers treat
// as "nothing extra allowed" rather than "everything allowed".
func getCSVEnv(key string) []string {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, strings.ToLower(p))
		}
	}
	return out
}

func getDuration(key string, fallback time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return fallback
}
