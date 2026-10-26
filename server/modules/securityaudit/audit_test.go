package securityaudit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/audit"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/config"
)

func evalCfg(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		JWTSecret:       "k9Qz-7tLm2Wv8xPa4Hr6Nc1Bd3Fy5Ge0Js",
		LogLevel:        "info",
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 7 * 24 * time.Hour,
		BackupDir:       t.TempDir(),
		BackupInterval:  time.Hour,
	}
}

func byID(r AuditReport, id int) CheckItem {
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	return CheckItem{}
}

// The audit used to report 26 of its 40 checks as "pass" with a fixed
// sentence of evidence, and its own test required at least 30 passes on an
// empty configuration -- the grade measured what the evaluator assumed, not
// what it checked. Checks it cannot observe are now listed as not verified
// and kept out of the score.
func TestOnlyEvaluatedChecksCountTowardTheGrade(t *testing.T) {
	report := NewEvaluator(evalCfg(t), credDB(t)).Evaluate(context.Background())

	if report.TotalChecks != 40 || len(report.Checks) != 40 {
		t.Fatalf("report lists %d checks, want all 40", report.TotalChecks)
	}
	if report.Evaluated+report.NotVerified != report.TotalChecks {
		t.Fatalf("evaluated %d + not verified %d != %d", report.Evaluated, report.NotVerified, report.TotalChecks)
	}
	if report.Passed+report.Warned+report.Failed != report.Evaluated {
		t.Fatalf("pass/warn/fail %d/%d/%d do not add up to %d evaluated", report.Passed, report.Warned, report.Failed, report.Evaluated)
	}
	if report.NotVerified < 20 {
		t.Fatalf("only %d checks are marked not verified; the code-level controls are being scored again", report.NotVerified)
	}
	for _, c := range report.Checks {
		if c.Status == StatusNotVerified && !strings.HasPrefix(c.Evidence, "Not checked by this audit") {
			t.Errorf("check %d is not verified but its evidence claims a finding: %q", c.ID, c.Evidence)
		}
		if c.Status == StatusPass && strings.Contains(c.Evidence, "Not checked") {
			t.Errorf("check %d passes with no finding", c.ID)
		}
	}
	// On a fresh install with no backup and no TLS, the grade cannot be A.
	if report.Grade == "A+" || report.Grade == "A" {
		t.Fatalf("a server with no backup and plain HTTP is graded %s", report.Grade)
	}
}

// A single failure caps the grade below C: a broken audit chain or a weak
// password hash is not offset by other checks passing.
func TestAFailureCapsTheGrade(t *testing.T) {
	d := credDB(t)
	ctx := context.Background()
	cfg := evalCfg(t)
	// Everything the audit can see is in order -- a fresh backup, HTTPS, an
	// intact audit chain, an administrator with a changed password -- except
	// one failure. Without the cap that still grades A.
	cfg.TLSCertFile, cfg.TLSKeyFile = "cert.pem", "key.pem"
	cfg.UpdateSigningPublicKey = "configured"
	if err := os.WriteFile(filepath.Join(cfg.BackupDir, "b.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	addUser(t, d, "henok", "admin", "a-long-unique-passphrase-9", now.Add(-time.Hour), now)
	if err := audit.Log(ctx, d, "user", "u1", "login", "", nil); err != nil {
		t.Fatal(err)
	}
	cfg.AllowedOriginDomains = []string{"*"}
	report := NewEvaluator(cfg, d).Evaluate(ctx)
	if report.Failed != 1 {
		for _, c := range report.Checks {
			if c.Status == StatusFail || c.Status == StatusWarn {
				t.Logf("%d %s: %s", c.ID, c.Status, c.Evidence)
			}
		}
		t.Fatalf("fixture has %d failures, want only the CORS one", report.Failed)
	}
	if byID(report, 22).Status != StatusFail {
		t.Fatalf("CORS with * is %s", byID(report, 22).Status)
	}
	if report.Score > 69 || report.Grade != "F" {
		t.Fatalf("with a failed check the score is %d (%s)", report.Score, report.Grade)
	}
}

// The audit chain is verified, not counted: an edited entry fails the check.
func TestAnEditedAuditEntryFailsTheChainCheck(t *testing.T) {
	d := credDB(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := audit.Log(ctx, d, "user", "u1", "device.retire", "dev-1", map[string]string{"n": "x"}); err != nil {
			t.Fatal(err)
		}
	}
	e := NewEvaluator(evalCfg(t), d)
	if c := e.checkTamperEvidentAuditChain(ctx); c.Status != StatusPass {
		t.Fatalf("an intact chain is %s: %s", c.Status, c.Evidence)
	}
	d.MustExec(`UPDATE audit_logs SET action = 'device.restore' WHERE seq = (SELECT MIN(seq) FROM audit_logs)`)
	if c := e.checkTamperEvidentAuditChain(ctx); c.Status != StatusFail {
		t.Fatalf("an edited audit entry left the chain check at %s: %s", c.Status, c.Evidence)
	}
}

// The backup check passed whenever a directory was configured. It now looks
// at the newest backup file.
func TestTheBackupCheckLooksAtTheNewestBackup(t *testing.T) {
	cfg := evalCfg(t)
	e := NewEvaluator(cfg, credDB(t))
	now := time.Now()
	if st, ev, _ := e.backupFindings(now); st != StatusWarn {
		t.Fatalf("an empty backup directory is %s: %s", st, ev)
	}
	f := filepath.Join(cfg.BackupDir, "endpoint-mgmt-2026.db")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, ev, _ := e.backupFindings(now); st != StatusPass {
		t.Fatalf("a fresh backup is %s: %s", st, ev)
	}
	old := now.Add(-3 * 24 * time.Hour)
	if err := os.Chtimes(f, old, old); err != nil {
		t.Fatal(err)
	}
	if st, ev, _ := e.backupFindings(now); st != StatusWarn {
		t.Fatalf("a three-day-old backup with hourly backups configured is %s: %s", st, ev)
	}
}

// Transport encryption is a real deployment property: plain HTTP with no
// trusted proxy is a warning, a configured certificate passes.
func TestPlainHTTPWithoutAProxyIsAWarning(t *testing.T) {
	cfg := evalCfg(t)
	e := NewEvaluator(cfg, credDB(t))
	if c := e.checkLiveSecuritySelfTest(); c.Status != StatusWarn {
		t.Fatalf("plain HTTP is %s", c.Status)
	}
	cfg.TLSCertFile, cfg.TLSKeyFile = "cert.pem", "key.pem"
	if c := e.checkLiveSecuritySelfTest(); c.Status != StatusPass {
		t.Fatalf("HTTPS is %s", c.Status)
	}
}

// Every stored hash is checked, not one sample.
func TestOneWeakPasswordHashFailsTheCheck(t *testing.T) {
	d := credDB(t)
	now := time.Now()
	addUser(t, d, "alice", "admin", "correct-horse-battery", now, now)
	d.MustExec(`INSERT INTO users (id, username, password_hash, role, created_at, updated_at) VALUES ('u-old', 'legacy', '5f4dcc3b5aa765d61d8327deb882cf99', 'viewer', ?, ?)`, now, now)
	c := NewEvaluator(evalCfg(t), d).checkPasswordHashing(context.Background())
	if c.Status != StatusFail || !strings.Contains(c.Evidence, "1 of 2") {
		t.Fatalf("a legacy MD5 hash next to a bcrypt one is %s: %s", c.Status, c.Evidence)
	}
}

// A long-lived access token is a warning.
func TestALongAccessTokenLifetimeIsAWarning(t *testing.T) {
	cfg := evalCfg(t)
	cfg.AccessTokenTTL = 24 * time.Hour
	if c := NewEvaluator(cfg, credDB(t)).checkSessionSecurity(context.Background()); c.Status != StatusWarn {
		t.Fatalf("24h access tokens are %s", c.Status)
	}
}
