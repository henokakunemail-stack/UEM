package securityaudit

import (
	"context"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/audit"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/config"
	"github.com/jmoiron/sqlx"
)

type Evaluator struct {
	cfg *config.Config
	db  *sqlx.DB
	// setupPassword is ADMIN_PASSWORD, for the default-credential check.
	setupPassword string
}

func NewEvaluator(cfg *config.Config, db *sqlx.DB) *Evaluator {
	return &Evaluator{cfg: cfg, db: db}
}

func (e *Evaluator) Evaluate(ctx context.Context) AuditReport {
	checks := make([]CheckItem, 0, 40)

	// ==========================================
	// PART 1: CORE APP & DATA SECURITY (1 - 20)
	// ==========================================

	// 1. API key aman
	checks = append(checks, e.checkAPIKeySecurity())

	// 2. env jangan public
	checks = append(checks, e.checkEnvNotPublic())

	// 3. No hardcode secret
	checks = append(checks, e.checkNoHardcodedSecret())

	// 4. Cek secret di Git
	checks = append(checks, e.checkSecretInGit())

	// 5. Debug mode OFF
	checks = append(checks, e.checkDebugModeOff())

	// 6. Error jangan bocor
	checks = append(checks, e.checkErrorLeakage())

	// 7. Validasi input
	checks = append(checks, e.checkInputValidation())

	// 8. Sanitasi input
	checks = append(checks, e.checkInputSanitization())

	// 9. Anti SQL injection
	checks = append(checks, e.checkAntiSQLInjection())

	// 10. Anti XSS
	checks = append(checks, e.checkAntiXSS())

	// 11. Server-side auth
	checks = append(checks, e.checkServerSideAuth())

	// 12. Cek akses user (RBAC)
	checks = append(checks, e.checkUserAccessRBAC())

	// 13. Role admin aman
	checks = append(checks, e.checkAdminRoleSecurity())

	// 14. DB jangan public
	checks = append(checks, e.checkDBNotPublic())

	// 15. DB permission ketat
	checks = append(checks, e.checkDBFilePermissions())

	// 16. Hash password
	checks = append(checks, e.checkPasswordHashing(ctx))

	// 17. Session aman
	checks = append(checks, e.checkSessionSecurity(ctx))

	// 18. Reset password aman
	checks = append(checks, e.checkResetPasswordSecurity())

	// 19. Batasi upload file
	checks = append(checks, e.checkFileUploadLimits())

	// 20. Scan upload file
	checks = append(checks, e.checkFileUploadScanning())

	// ====================================================
	// PART 2: NETWORK, PIPELINE & HARDENING (21 - 40)
	// ====================================================

	// 21. Rate limit API
	checks = append(checks, e.checkRateLimitAPI())

	// 22. CORS ketat
	checks = append(checks, e.checkStrictCORS())

	// 23. Proteksi CSRF
	checks = append(checks, e.checkCSRFProtection())

	// 24. Cegah SSRF
	checks = append(checks, e.checkPreventSSRF())

	// 25. Path traversal
	checks = append(checks, e.checkPathTraversalJail())

	// 26. Validasi input API
	checks = append(checks, e.checkAPIInputValidation())

	// 27. Validasi output API
	checks = append(checks, e.checkAPIOutputValidation())

	// 28. Admin route aman
	checks = append(checks, e.checkAdminRouteSecurity())

	// 29. Ganti default pass
	checks = append(checks, e.checkDefaultPasswordChanged(ctx))

	// 30. Audit chain integrity
	checks = append(checks, e.checkTamperEvidentAuditChain(ctx))

	// 31. Verifikasi webhook
	checks = append(checks, e.checkWebhookVerification())

	// 32. Cek payment / license server
	checks = append(checks, e.checkLicenseCompliance(ctx))

	// 33. Harga / release anti-tamper
	checks = append(checks, e.checkReleaseAntiTamper())

	// 34. Cek IDOR
	checks = append(checks, e.checkIDORProtection())

	// 35. Log jangan bocor
	checks = append(checks, e.checkLogSanitization())

	// 36. Private source map
	checks = append(checks, e.checkPrivateSourceMap())

	// 37. Update dependency
	checks = append(checks, e.checkDependencyHygiene())

	// 38. Recent backup
	checks = append(checks, e.checkBackupRestoreCapability())

	// 39. Security headers
	checks = append(checks, e.checkSecurityHeaders())

	// 40. Transport encryption
	checks = append(checks, e.checkLiveSecuritySelfTest())

	// Aggregate statistics. Only evaluated checks count toward the score: a
	// control the audit cannot observe says nothing about this deployment,
	// and scoring it as a pass is what let the grade claim more than had been
	// checked.
	passed, warned, failed, notVerified := 0, 0, 0, 0
	for _, c := range checks {
		switch c.Status {
		case StatusPass:
			passed++
		case StatusWarn:
			warned++
		case StatusFail:
			failed++
		case StatusNotVerified:
			notVerified++
		}
	}

	total := len(checks)
	evaluated := passed + warned + failed
	score := 0
	if evaluated > 0 {
		// Pass = full marks, warn = half, fail = none.
		score = int(math.Round((float64(passed) + float64(warned)/2) * 100 / float64(evaluated)))
	}
	// A single failure caps the grade: an exposed default password is not
	// offset by twelve other checks passing.
	if failed > 0 {
		score = min(score, 69)
	}

	var grade string
	switch {
	case score >= 95:
		grade = "A+"
	case score >= 88:
		grade = "A"
	case score >= 80:
		grade = "B"
	case score >= 70:
		grade = "C"
	default:
		grade = "F"
	}

	return AuditReport{
		GeneratedAt: time.Now().UTC(),
		Grade:       grade,
		Score:       score,
		TotalChecks: total,
		Passed:      passed,
		Warned:      warned,
		Failed:      failed,
		NotVerified: notVerified,
		Evaluated:   evaluated,
		Checks:      checks,
	}
}

// notVerified lists a control the audit cannot observe on a running server:
// how handlers validate input, which routes carry which role, what the
// frontend escapes. These used to be reported as "pass" with a fixed sentence
// of evidence, some of it no longer true, and they made up most of the score.
// They are still listed, as requirements with a way to review them, but they
// do not count toward the grade.
func notVerified(id, part int, title string, cat CheckCategory, owasp, requirement, review string) CheckItem {
	return CheckItem{
		ID:          id,
		Part:        part,
		Number:      id,
		Title:       title,
		Category:    cat,
		Status:      StatusNotVerified,
		Description: requirement,
		Evidence:    "Not checked by this audit: this is a property of the server's code, not of this deployment, and it cannot be observed at runtime.",
		Remediation: review,
		OWASPTag:    owasp,
	}
}

// -------------------------------------------------------------
// Part 1 Implementations
// -------------------------------------------------------------

func (e *Evaluator) checkAPIKeySecurity() CheckItem {
	return notVerified(1, 1, "API Key & Token Hashing", CategoryCoreApp, "A02:2021-Cryptographic Failures",
		"API keys, device secrets and one-time tickets must never be stored in plaintext.",
		"Review how device secrets and WebSocket tickets are stored (server/modules/device-management, server/core/auth).")
}

func (e *Evaluator) checkEnvNotPublic() CheckItem {
	return notVerified(2, 1, "Environment File Privacy", CategoryCoreApp, "A05:2021-Security Misconfiguration",
		"Configuration files (.env, server.env) must live outside anything served over HTTP.",
		"Request /server.env and /.env through the public URL and confirm a 404; make the reverse proxy refuse dotfiles.")
}

func (e *Evaluator) checkNoHardcodedSecret() CheckItem {
	status := StatusPass
	evidence := fmt.Sprintf("JWT_SECRET configured with %d bytes length.", len(e.cfg.JWTSecret))
	remediation := "No action needed; high entropy secret is active."

	if len(e.cfg.JWTSecret) < 32 {
		status = StatusWarn
		evidence = fmt.Sprintf("JWT_SECRET length is %d bytes (recommended >= 32 bytes for HMAC-SHA256).", len(e.cfg.JWTSecret))
		remediation = "Generate a cryptographically secure 256-bit secret using `openssl rand -base64 32`."
	}
	if strings.Contains(strings.ToLower(e.cfg.JWTSecret), "secret") || strings.Contains(strings.ToLower(e.cfg.JWTSecret), "default") {
		status = StatusFail
		evidence = "JWT_SECRET contains common default terms ('secret' or 'default')."
		remediation = "Replace default development secret immediately before production launch."
	}

	return CheckItem{
		ID:          3,
		Part:        1,
		Number:      3,
		Title:       "Cryptographic Secret Entropy",
		Category:    CategoryCoreApp,
		Status:      status,
		Description: "Secrets and signing keys must possess sufficient entropy and never rely on hardcoded fallback keys.",
		Evidence:    evidence,
		Remediation: remediation,
		OWASPTag:    "A02:2021-Cryptographic Failures",
	}
}

func (e *Evaluator) checkSecretInGit() CheckItem {
	return notVerified(4, 1, "Version Control Secret Exclusion", CategoryCoreApp, "A05:2021-Security Misconfiguration",
		"Credentials, environment files and databases must never be committed to version control.",
		"Scan the repository and its history with gitleaks or trufflehog before publishing it.")
}

func (e *Evaluator) checkDebugModeOff() CheckItem {
	status := StatusPass
	evidence := fmt.Sprintf("Server logging configured at level '%s'. pprof endpoints are unexposed in production builds.", e.cfg.LogLevel)
	remediation := "Maintain info/warn logging in production environments."

	if strings.ToLower(e.cfg.LogLevel) == "debug" || strings.ToLower(e.cfg.LogLevel) == "trace" {
		status = StatusWarn
		evidence = fmt.Sprintf("Server is currently running with verbose log level '%s'.", e.cfg.LogLevel)
		remediation = "Set LOG_LEVEL=info or LOG_LEVEL=warn to reduce log volume and prevent sensitive data logging."
	}

	return CheckItem{
		ID:          5,
		Part:        1,
		Number:      5,
		Title:       "Debug Mode & Profiler Deactivation",
		Category:    CategoryCoreApp,
		Status:      status,
		Description: "Diagnostic debug modes and pprof runtime profilers must be disabled in production deployments to prevent profiling exposure.",
		Evidence:    evidence,
		Remediation: remediation,
		OWASPTag:    "A05:2021-Security Misconfiguration",
	}
}

func (e *Evaluator) checkErrorLeakage() CheckItem {
	return notVerified(6, 1, "API Error Sanitization", CategoryCoreApp, "A04:2021-Insecure Design",
		"Error responses must not leak SQL, file paths or stack traces.",
		"Review handler error paths; probe the API with malformed requests and read the responses.")
}

func (e *Evaluator) checkInputValidation() CheckItem {
	return notVerified(7, 1, "Request Body & Boundary Limits", CategoryCoreApp, "A04:2021-Insecure Design",
		"Request bodies, especially unauthenticated ones, must be size-limited.",
		"Review http.MaxBytesReader use on login, enrollment and upload handlers.")
}

func (e *Evaluator) checkInputSanitization() CheckItem {
	return notVerified(8, 1, "Command & Argument Sanitization", CategoryCoreApp, "A03:2021-Injection",
		"Commands must be run with separate arguments, never by building a shell string from input.",
		"Review the agent's exec paths (remote-exec, software install, maintenance, system policy).")
}

func (e *Evaluator) checkAntiSQLInjection() CheckItem {
	return notVerified(9, 1, "Parameterized SQL Execution", CategoryData, "A03:2021-Injection",
		"Every query must bind values as parameters instead of formatting them into SQL.",
		"Search repositories for fmt.Sprintf or string concatenation that builds SQL.")
}

func (e *Evaluator) checkAntiXSS() CheckItem {
	return notVerified(10, 1, "Cross-Site Scripting (XSS) Prevention", CategoryCoreApp, "A03:2021-Injection",
		"Untrusted content must be escaped on output, and a Content-Security-Policy should limit script sources.",
		"Search the console for dangerouslySetInnerHTML; check the Content-Security-Policy response header.")
}

func (e *Evaluator) checkServerSideAuth() CheckItem {
	return notVerified(11, 1, "Server-Side Token Authentication", CategoryCoreApp, "A07:2021-Identification and Authentication Failures",
		"Every private API route must verify the caller's token on the server.",
		"Review route registration for routes outside the authentication middleware.")
}

func (e *Evaluator) checkUserAccessRBAC() CheckItem {
	return notVerified(12, 1, "Role-Based Access Control (RBAC)", CategoryCoreApp, "A01:2021-Broken Access Control",
		"State-changing routes must require the role the action needs (viewer, technician, admin).",
		"Review rbac.RequireRole on each route; test each role against the routes it must not reach.")
}

func (e *Evaluator) checkAdminRoleSecurity() CheckItem {
	return notVerified(13, 1, "Administrative Privilege Separation", CategoryCoreApp, "A01:2021-Broken Access Control",
		"User management, backup and restore and permanent deletion must require the admin role.",
		"Sign in as a technician and confirm those actions are refused.")
}

func (e *Evaluator) checkDBNotPublic() CheckItem {
	status := StatusPass
	evidence := "SQLite database is a local file on server filesystem with no network listening socket."
	remediation := "If using PostgreSQL, bind to 127.0.0.1 or an isolated private VPC network only."

	if e.cfg.DBDriver == "postgres" || strings.HasPrefix(e.cfg.DBURL, "postgres://") {
		if strings.Contains(e.cfg.DBURL, "0.0.0.0") {
			status = StatusWarn
			evidence = "PostgreSQL DBURL references 0.0.0.0."
			remediation = "Bind PostgreSQL to private network or localhost only."
		} else {
			evidence = "PostgreSQL connected via configured network host."
		}
	}

	return CheckItem{
		ID:          14,
		Part:        1,
		Number:      14,
		Title:       "Database Network Isolation",
		Category:    CategoryData,
		Status:      status,
		Description: "Database storage must never be bound to public network interfaces or accessible from untrusted networks.",
		Evidence:    evidence,
		Remediation: remediation,
		OWASPTag:    "A05:2021-Security Misconfiguration",
	}
}

func (e *Evaluator) checkDBFilePermissions() CheckItem {
	status, evidence, remediation := e.dbFilePermissionFindings(runtime.GOOS)

	return CheckItem{
		ID:          15,
		Part:        1,
		Number:      15,
		Title:       "Database File Access Permissions",
		Category:    CategoryData,
		Status:      status,
		Description: "Database files must only be readable and writable by the dedicated service account (chmod 0600 on Linux / restricted ACL on Windows).",
		Evidence:    evidence,
		Remediation: remediation,
		OWASPTag:    "A01:2021-Broken Access Control",
	}
}

func (e *Evaluator) checkPasswordHashing(ctx context.Context) CheckItem {
	status := StatusPass
	var evidence string
	remediation := "Keep bcrypt cost >= 12 or migrate to Argon2id."

	// Every hash is checked, not one sample: one account created by an old
	// import with a weak hash is the one an attacker finds.
	var users, weak int
	errAll := e.db.GetContext(ctx, &users, `SELECT COUNT(*) FROM users WHERE password_hash != ''`)
	errWeak := e.db.GetContext(ctx, &weak, `SELECT COUNT(*) FROM users WHERE password_hash != '' AND password_hash NOT LIKE '$2a$%' AND password_hash NOT LIKE '$2b$%' AND password_hash NOT LIKE '$2y$%'`)
	switch {
	case errAll != nil || errWeak != nil:
		status = StatusWarn
		evidence = "The users table could not be read to check password hashes."
		remediation = "Check the database connection and rerun the audit."
	case weak > 0:
		status = StatusFail
		evidence = fmt.Sprintf("%d of %d stored password(s) are not bcrypt hashes.", weak, users)
		remediation = "Reset the passwords of those accounts so they are re-hashed with bcrypt."
	case users == 0:
		evidence = "No console accounts have a password yet."
	default:
		evidence = fmt.Sprintf("All %d stored password(s) are bcrypt hashes.", users)
	}

	return CheckItem{
		ID:          16,
		Part:        1,
		Number:      16,
		Title:       "Adaptive Password Hashing",
		Category:    CategoryData,
		Status:      status,
		Description: "User passwords must be hashed using an adaptive, computationally expensive algorithm (bcrypt >= 12 or Argon2id).",
		Evidence:    evidence,
		Remediation: remediation,
		OWASPTag:    "A02:2021-Cryptographic Failures",
	}
}

func (e *Evaluator) checkSessionSecurity(ctx context.Context) CheckItem {
	_ = ctx
	access, refresh := e.cfg.AccessTokenTTL, e.cfg.RefreshTokenTTL
	status := StatusPass
	evidence := fmt.Sprintf("Access tokens last %s and refresh sessions %s; sessions are stored server-side and revoked on logout.", access, refresh)
	remediation := "Keep access tokens short-lived (15-60 min)."
	switch {
	case access <= 0 || refresh <= 0:
		status = StatusWarn
		evidence = "ACCESS_TOKEN_TTL or REFRESH_TOKEN_TTL is not set to a positive duration."
		remediation = "Set ACCESS_TOKEN_TTL (e.g. 15m) and REFRESH_TOKEN_TTL (e.g. 168h)."
	case access > time.Hour:
		status = StatusWarn
		evidence = fmt.Sprintf("Access tokens last %s: a stolen token stays usable that long, and revocation only takes effect when it expires.", access)
		remediation = "Lower ACCESS_TOKEN_TTL to 1h or less."
	case refresh > 30*24*time.Hour:
		status = StatusWarn
		evidence = fmt.Sprintf("Refresh sessions last %s.", refresh)
		remediation = "Lower REFRESH_TOKEN_TTL to 30 days or less."
	}

	return CheckItem{
		ID:          17,
		Part:        1,
		Number:      17,
		Title:       "Session Lifetime & Revocation",
		Category:    CategoryCoreApp,
		Status:      status,
		Description: "Access tokens must be short-lived and sessions revocable on the server.",
		Evidence:    evidence,
		Remediation: remediation,
		OWASPTag:    "A07:2021-Identification and Authentication Failures",
	}
}

func (e *Evaluator) checkResetPasswordSecurity() CheckItem {
	return notVerified(18, 1, "Secure Password Change Flow", CategoryCoreApp, "A07:2021-Identification and Authentication Failures",
		"Changing a password must require the current one and end the account's other sessions.",
		"Change a password while signed in elsewhere and confirm the other session ends.")
}

func (e *Evaluator) checkFileUploadLimits() CheckItem {
	return notVerified(19, 1, "File Upload Size Restraints", CategoryCoreApp, "A04:2021-Insecure Design",
		"Uploads must be size-limited so they cannot fill the disk.",
		"Review upload handlers for size limits; monitor free space on the data volume.")
}

func (e *Evaluator) checkFileUploadScanning() CheckItem {
	return notVerified(20, 1, "File Upload Integrity & Validation", CategoryCoreApp, "A08:2021-Software and Data Integrity Failures",
		"Uploaded packages must be checksummed, and ideally scanned, before they are distributed.",
		"Uploaded installers are not malware-scanned; scan packages before uploading them.")
}

// -------------------------------------------------------------
// Part 2 Implementations
// -------------------------------------------------------------

func (e *Evaluator) checkRateLimitAPI() CheckItem {
	return notVerified(21, 2, "API Rate Limiting & Brute-Force Defense", CategoryNetwork, "A04:2021-Insecure Design",
		"Login and enrollment must be rate-limited against brute force.",
		"Send repeated failed logins and confirm HTTP 429 and account lockout.")
}

func (e *Evaluator) checkStrictCORS() CheckItem {
	status := StatusPass
	evidence := "No extra origins are allowed: only the console's own origin can call the API from a browser."
	remediation := "Only add trusted domains to ALLOWED_ORIGIN_DOMAINS."

	for _, d := range e.cfg.AllowedOriginDomains {
		if strings.TrimSpace(d) == "*" {
			status = StatusFail
			evidence = "ALLOWED_ORIGIN_DOMAINS contains \"*\": any website can call the API from a signed-in operator's browser."
			remediation = "Remove \"*\" and list the exact domains that need access."
			break
		}
	}
	if status == StatusPass && len(e.cfg.AllowedOriginDomains) > 0 {
		evidence = fmt.Sprintf("Allowed extra origins: %s.", strings.Join(e.cfg.AllowedOriginDomains, ", "))
	}

	return CheckItem{
		ID:          22,
		Part:        2,
		Number:      22,
		Title:       "Strict Cross-Origin Resource Sharing (CORS)",
		Category:    CategoryNetwork,
		Status:      status,
		Description: "Cross-origin requests and WebSocket origins must be limited to trusted domains.",
		Evidence:    evidence,
		Remediation: remediation,
		OWASPTag:    "A01:2021-Broken Access Control",
	}
}

func (e *Evaluator) checkCSRFProtection() CheckItem {
	return notVerified(23, 2, "Cross-Site Request Forgery (CSRF) Mitigation", CategoryNetwork, "A01:2021-Broken Access Control",
		"Authentication must not rely on cookies a browser sends automatically, or must carry CSRF protection.",
		"Confirm API calls authenticate with an Authorization header rather than a cookie.")
}

func (e *Evaluator) checkPreventSSRF() CheckItem {
	return notVerified(24, 2, "Server-Side Request Forgery (SSRF) Prevention", CategoryNetwork, "A10:2021-Server-Side Request Forgery",
		"Server-side requests to operator-supplied URLs (webhooks) must not reach internal addresses.",
		"Point an alert webhook at 127.0.0.1 or a private address and confirm it is refused.")
}

func (e *Evaluator) checkPathTraversalJail() CheckItem {
	return notVerified(25, 2, "Path Traversal & Storage Jailing", CategoryHardening, "A01:2021-Broken Access Control",
		"File operations on supplied names must stay inside their storage directory.",
		"Review file name handling in package, backup and transfer storage.")
}

func (e *Evaluator) checkAPIInputValidation() CheckItem {
	return notVerified(26, 2, "Strict API Schema Validation", CategoryPipeline, "A04:2021-Insecure Design",
		"Requests must be validated and malformed ones rejected with HTTP 400.",
		"Probe endpoints with missing and malformed fields.")
}

func (e *Evaluator) checkAPIOutputValidation() CheckItem {
	return notVerified(27, 2, "Sensitive Field Redaction in API Responses", CategoryPipeline, "A02:2021-Cryptographic Failures",
		"Password hashes, device secrets and keys must never appear in API responses.",
		"Read the user and device API responses and confirm no secret fields are present.")
}

func (e *Evaluator) checkAdminRouteSecurity() CheckItem {
	return notVerified(28, 2, "Administrative Route Isolation", CategoryHardening, "A01:2021-Broken Access Control",
		"Administrative endpoints must sit behind the admin role.",
		"Review route registration for administrative endpoints outside rbac.RequireRole(RoleAdmin).")
}

func (e *Evaluator) checkDefaultPasswordChanged(ctx context.Context) CheckItem {
	status, evidence, remediation := e.adminCredentialFindings(ctx)

	return CheckItem{
		ID:          29,
		Part:        2,
		Number:      29,
		Title:       "Default Credential Invalidation",
		Category:    CategoryHardening,
		Status:      status,
		Description: "Default administrative passwords provided at install time must be changed prior to deploying production endpoints.",
		Evidence:    evidence,
		Remediation: remediation,
		OWASPTag:    "A07:2021-Identification and Authentication Failures",
	}
}

func (e *Evaluator) checkTamperEvidentAuditChain(ctx context.Context) CheckItem {
	status := StatusPass
	var evidence string
	remediation := "Periodically export the audit log to external storage."

	// The chain is verified, not counted: a row count says nothing about
	// whether a row was edited or deleted.
	rep, err := audit.VerifyChain(ctx, e.db)
	switch {
	case err != nil:
		status = StatusWarn
		evidence = "The audit chain could not be verified: " + err.Error()
		remediation = "Check the database connection and rerun the audit."
	case !rep.OK:
		status = StatusFail
		evidence = fmt.Sprintf("The audit chain is broken at entry %s: %s.", rep.BreakID, rep.Reason)
		remediation = "Investigate who changed the audit log; compare against an exported copy."
	case rep.Checked == 0:
		status = StatusWarn
		evidence = "The audit log has no chained entries to verify yet."
		remediation = "Entries are chained as they are written; rerun the audit after some activity."
	default:
		evidence = fmt.Sprintf("%d chained audit entries verified intact.", rep.Checked)
	}

	return CheckItem{
		ID:          30,
		Part:        2,
		Number:      30,
		Title:       "Tamper-Evident Audit Logging",
		Category:    CategoryPipeline,
		Status:      status,
		Description: "Administrative actions must be recorded in an audit log whose integrity can be verified.",
		Evidence:    evidence,
		Remediation: remediation,
		OWASPTag:    "A09:2021-Security Logging and Monitoring Failures",
	}
}

func (e *Evaluator) checkWebhookVerification() CheckItem {
	return notVerified(31, 2, "Webhook Authenticity", CategoryPipeline, "A08:2021-Software and Data Integrity Failures",
		"Receivers should be able to verify that an alert webhook came from this server.",
		"Confirm whether your webhook receiver can authenticate the sender; restrict it to this server's address otherwise.")
}

func (e *Evaluator) checkLicenseCompliance(ctx context.Context) CheckItem {
	status := StatusPass
	remediation := "Review license allocations and expiry under Asset & License management."

	var over, expired int
	errOver := e.db.GetContext(ctx, &over, `
		SELECT COUNT(*) FROM software_licenses l
		 WHERE (SELECT COUNT(*) FROM license_allocations a WHERE a.license_id = l.id) > l.total_seats`)
	errExp := e.db.GetContext(ctx, &expired, `
		SELECT COUNT(*) FROM software_licenses l
		 WHERE l.expires_at IS NOT NULL AND l.expires_at < CURRENT_TIMESTAMP
		   AND EXISTS (SELECT 1 FROM license_allocations a WHERE a.license_id = l.id)`)
	var evidence string
	switch {
	case errOver != nil || errExp != nil:
		status = StatusWarn
		evidence = "License data could not be read."
		remediation = "Check the database connection and rerun the audit."
	case over > 0 || expired > 0:
		status = StatusWarn
		evidence = fmt.Sprintf("%d license(s) allocated beyond their seats; %d expired license(s) still allocated.", over, expired)
	default:
		evidence = "No license is allocated beyond its seats or used after expiry."
	}

	return CheckItem{
		ID:          32,
		Part:        2,
		Number:      32,
		Title:       "Asset & License Compliance",
		Category:    CategoryHardening,
		Status:      status,
		Description: "Software must not be deployed beyond licensed seats or after the license expires.",
		Evidence:    evidence,
		Remediation: remediation,
		OWASPTag:    "A05:2021-Security Misconfiguration",
	}
}

func (e *Evaluator) checkReleaseAntiTamper() CheckItem {
	status := StatusPass
	var evidence string
	remediation := "Configure Ed25519 signing key in server config for cryptographic agent update verification."

	if e.cfg.UpdateSigningPublicKey != "" {
		evidence = "Cryptographic Ed25519 public key configured for agent update signature verification."
	} else {
		status = StatusWarn
		evidence = "UpdateSigningPublicKey not configured; updates verified via SHA-256 checksum."
		remediation = "Provide UPDATE_SIGNING_PUBLIC_KEY in server.env to enforce Ed25519 signature checks on updates."
	}

	return CheckItem{
		ID:          33,
		Part:        2,
		Number:      33,
		Title:       "Release Artifact Anti-Tamper Verification",
		Category:    CategoryPipeline,
		Status:      status,
		Description: "Update payloads and release binaries must verify cryptographic signatures and SHA-256 checksums prior to installation.",
		Evidence:    evidence,
		Remediation: remediation,
		OWASPTag:    "A08:2021-Software and Data Integrity Failures",
	}
}

func (e *Evaluator) checkIDORProtection() CheckItem {
	return notVerified(34, 2, "Insecure Direct Object Reference (IDOR) Defense", CategoryHardening, "A01:2021-Broken Access Control",
		"An identifier in a URL must not let a caller reach a record they are not entitled to.",
		"Test agent endpoints with one device's credentials against another device's id.")
}

func (e *Evaluator) checkLogSanitization() CheckItem {
	return notVerified(35, 2, "Log Credential & Token Redaction", CategoryHardening, "A09:2021-Security Logging and Monitoring Failures",
		"Tokens, passwords and keys must not be written to logs.",
		"Search the server and agent logs for tokens, secrets and Authorization headers.")
}

func (e *Evaluator) checkPrivateSourceMap() CheckItem {
	return notVerified(36, 2, "Frontend Source Map Privacy", CategoryPipeline, "A05:2021-Security Misconfiguration",
		"Production frontend builds should not ship source maps.",
		"Request a script URL with .map appended and confirm a 404.")
}

func (e *Evaluator) checkDependencyHygiene() CheckItem {
	return notVerified(37, 2, "Dependency Vulnerability Management", CategoryPipeline, "A06:2021-Vulnerable and Outdated Components",
		"Dependencies must be kept current and checked against known vulnerabilities.",
		"Run govulncheck ./... and npm audit for each release.")
}

func (e *Evaluator) checkBackupRestoreCapability() CheckItem {
	status, evidence, remediation := e.backupFindings(time.Now())
	return CheckItem{
		ID:          38,
		Part:        2,
		Number:      38,
		Title:       "Recent Database Backup",
		Category:    CategoryPipeline,
		Status:      status,
		Description: "A recent backup of the database must exist so the server can be restored.",
		Evidence:    evidence,
		Remediation: remediation,
		OWASPTag:    "A04:2021-Insecure Design",
	}
}

// backupFindings looks for the newest backup file. The check used to pass
// whenever a backup directory was configured, whether or not anything had
// ever been written to it.
func (e *Evaluator) backupFindings(now time.Time) (CheckStatus, string, string) {
	if e.cfg.DBDriver == "postgres" || strings.HasPrefix(e.cfg.DBURL, "postgres") {
		return StatusWarn, "The database is PostgreSQL; the server's built-in backups cover SQLite only.",
			"Back up PostgreSQL with pg_dump or your database's own backup service."
	}
	if e.cfg.BackupDir == "" {
		return StatusWarn, "No backup directory is configured.", "Set BACKUP_DIR."
	}
	entries, err := os.ReadDir(e.cfg.BackupDir)
	if err != nil {
		return StatusWarn, fmt.Sprintf("The backup directory %s cannot be read: %v.", e.cfg.BackupDir, err),
			"Check that BACKUP_DIR exists and is readable by the service."
	}
	var newest time.Time
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".db") {
			continue
		}
		if info, err := ent.Info(); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if newest.IsZero() {
		return StatusWarn, fmt.Sprintf("No backup file found in %s.", e.cfg.BackupDir),
			"Create a backup from Settings > Backup & restore, and check that scheduled backups run."
	}
	// Scheduled backups run every BACKUP_INTERVAL; two missed runs and a
	// margin is old.
	limit := 24 * time.Hour
	if e.cfg.BackupInterval > 0 {
		limit = 2*e.cfg.BackupInterval + time.Hour
	}
	age := now.Sub(newest).Round(time.Minute)
	if age > limit {
		return StatusWarn, fmt.Sprintf("The newest backup in %s is %s old.", e.cfg.BackupDir, age),
			"Check that scheduled backups are running (BACKUP_INTERVAL) and that the disk is not full."
	}
	return StatusPass, fmt.Sprintf("The newest backup in %s is %s old.", e.cfg.BackupDir, age),
		"Test a restore periodically; a backup that has never been restored is not proven."
}

func (e *Evaluator) checkSecurityHeaders() CheckItem {
	return notVerified(39, 2, "HTTP Security Headers Hardening", CategoryNetwork, "A05:2021-Security Misconfiguration",
		"Responses should carry HSTS, X-Content-Type-Options, X-Frame-Options and a Content-Security-Policy.",
		"Inspect the response headers of the public URL, e.g. curl -I https://your-server/.")
}

func (e *Evaluator) checkLiveSecuritySelfTest() CheckItem {
	status := StatusPass
	var evidence string
	remediation := "Keep TLS in place for every route, including agent WebSockets."
	switch {
	case e.cfg.TLSCertFile != "" && e.cfg.TLSKeyFile != "":
		evidence = "The server serves HTTPS itself (TLS_CERT_FILE and TLS_KEY_FILE are set)."
	case len(e.cfg.TrustedProxies) > 0:
		evidence = fmt.Sprintf("The server serves plain HTTP behind a trusted reverse proxy (%s), which is expected to terminate TLS.",
			strings.Join(e.cfg.TrustedProxies, ", "))
		remediation = "Confirm the reverse proxy serves HTTPS only and that the server's own port is not reachable from outside."
	default:
		status = StatusWarn
		evidence = "The server serves plain HTTP and no trusted reverse proxy is configured: passwords, tokens and remote sessions cross the network unencrypted unless something in front of it adds TLS."
		remediation = "Set TLS_CERT_FILE and TLS_KEY_FILE, or put the server behind an HTTPS reverse proxy and set TRUSTED_PROXIES."
	}
	return CheckItem{
		ID:          40,
		Part:        2,
		Number:      40,
		Title:       "Transport Encryption (TLS)",
		Category:    CategoryNetwork,
		Status:      status,
		Description: "Console, API and agent traffic must be encrypted in transit.",
		Evidence:    evidence,
		Remediation: remediation,
		OWASPTag:    "A02:2021-Cryptographic Failures",
	}
}
