package securityaudit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/config"
)

// commonPasswords are tried against every administrator hash. The list is
// short on purpose: each comparison is a bcrypt verification (~0.25s at cost
// 12), and these are the guesses an attacker makes first.
var commonPasswords = []string{"admin", "password", "admin123", "changeme", "123456"}

// WithSetupPassword tells the evaluator the ADMIN_PASSWORD the server was
// started with. bootstrapAdmin writes it over the admin's password on every
// start, so an admin whose hash matches it is logging in with a password kept
// in plain text in the environment file.
func (e *Evaluator) WithSetupPassword(p string) *Evaluator {
	e.setupPassword = p
	return e
}

type adminRow struct {
	Username  string    `db:"username"`
	Hash      string    `db:"password_hash"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// adminCredentialFindings checks every administrator account for the
// credentials an installation starts with. It used to compare the hash with a
// truncated literal that no bcrypt hash can equal, so it reported a pass for
// every installation, including one still on its setup password.
func (e *Evaluator) adminCredentialFindings(ctx context.Context) (CheckStatus, string, string) {
	var admins []adminRow
	err := e.db.SelectContext(ctx, &admins,
		e.db.Rebind(`SELECT username, password_hash, created_at, updated_at FROM users WHERE role = ?`), "admin")
	if err != nil {
		return StatusWarn, "Administrator accounts could not be read, so their credentials were not checked: " + err.Error(),
			"Re-run the audit; if it persists, check the server log for database errors."
	}
	if len(admins) == 0 {
		return StatusWarn, "No administrator account exists, so there was nothing to check.",
			"Create an administrator account with a strong, unique password."
	}

	var weak, setup, unchanged []string
	for _, a := range admins {
		for _, p := range commonPasswords {
			if auth.ComparePassword(a.Hash, p) == nil {
				weak = append(weak, fmt.Sprintf("%s (%q)", a.Username, p))
				break
			}
		}
		if e.setupPassword != "" && auth.ComparePassword(a.Hash, e.setupPassword) == nil {
			setup = append(setup, a.Username)
		}
		// A row never updated since it was created still holds the password it
		// was created with. A row that was updated may or may not have had its
		// password changed, so nothing is claimed about it.
		if e.setupPassword == "" && a.Username == "admin" && !a.UpdatedAt.After(a.CreatedAt.Add(time.Second)) {
			unchanged = append(unchanged, a.Username)
		}
	}

	switch {
	case len(weak) > 0:
		return StatusFail,
			"Administrator account(s) use a common password: " + strings.Join(weak, ", ") + ".",
			"Change these passwords now under Users & Access; anyone can guess them."
	case len(setup) > 0:
		return StatusWarn,
			"Administrator account(s) " + strings.Join(setup, ", ") + " still log in with ADMIN_PASSWORD from the server environment. " +
				"That value is stored in plain text and is written over the admin password on every server restart.",
			"Change the admin password under Users & Access, then remove ADMIN_PASSWORD from server.env so a restart does not reset it."
	case len(unchanged) > 0:
		return StatusWarn,
			"The generated setup password of the admin account (printed once to the server log at install) has never been changed.",
			"Change the admin password under Users & Access, and remove the setup line from old server logs."
	}
	return StatusPass,
		fmt.Sprintf("%d administrator account(s) checked: none uses a common password, the ADMIN_PASSWORD setup value, or an unchanged generated setup password.", len(admins)),
		"Keep administrator passwords unique and rotate them when staff change."
}

// dbFilePermissionFindings checks who can read the SQLite database. It used to
// report a pass whatever the mode was; a database readable by every local
// account (password hashes, device secrets, the audit trail) passed.
func (e *Evaluator) dbFilePermissionFindings(goos string) (CheckStatus, string, string) {
	if e.cfg.DBURL != "" || e.cfg.DBDriver == "postgres" {
		return StatusPass,
			"Not applicable: the server uses PostgreSQL, whose data files are owned and protected by the database server.",
			"Restrict PostgreSQL access in pg_hba.conf to the server's address and a dedicated role."
	}
	dbPath := e.cfg.DBPath
	if dbPath == "" {
		dbPath = config.DBPathOrDefault()
	}
	if goos == "windows" {
		return StatusWarn,
			fmt.Sprintf("Not verified: Windows access control lists are not inspected by this audit (database %s).", dbPath),
			fmt.Sprintf("Confirm with `icacls \"%s\"` that only SYSTEM, Administrators and the service account have access.", dbPath)
	}

	worst := StatusPass
	var notes []string
	checked := 0
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		fi, err := os.Stat(p)
		if err != nil {
			if p == dbPath {
				return StatusWarn, fmt.Sprintf("Database file %s could not be read: %v.", dbPath, err),
					"Check DB_PATH and that the server can reach its database file."
			}
			continue
		}
		checked++
		st, note := classifyDBMode(filepath.Base(p), fi.Mode().Perm())
		notes = append(notes, note)
		if rank(st) > rank(worst) {
			worst = st
		}
	}
	evidence := strings.Join(notes, " ")
	if worst == StatusPass {
		return StatusPass, fmt.Sprintf("%d database file(s) readable only by their owner. %s", checked, evidence),
			"Keep the database owned by the service account with mode 0600."
	}
	return worst, evidence,
		fmt.Sprintf("Run `chmod 600 %s*` and make sure the files are owned by the service account.", dbPath)
}

// classifyDBMode grades one file's permission bits: readable by any local
// account is a failure, by the group a warning.
func classifyDBMode(name string, mode os.FileMode) (CheckStatus, string) {
	switch {
	case mode&0o006 != 0:
		return StatusFail, fmt.Sprintf("%s is %04o: every local account can access it.", name, mode)
	case mode&0o060 != 0:
		return StatusWarn, fmt.Sprintf("%s is %04o: its group can access it.", name, mode)
	}
	return StatusPass, fmt.Sprintf("%s is %04o.", name, mode)
}

func rank(s CheckStatus) int {
	switch s {
	case StatusFail:
		return 2
	case StatusWarn:
		return 1
	}
	return 0
}
