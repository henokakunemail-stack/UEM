package systempolicy

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// encPrefix marks a password sealed with AES-256-GCM under the repository's
// encKey. A stored value without it is plaintext written by a server version
// before this encryption existed; decryptPassword returns it as-is, and the
// next write that touches the row re-encrypts it (see encryptPassword at the
// Create/Update call sites in repository.go).
const encPrefix = "aesgcm:v1:"

// encryptPassword seals plain with AES-256-GCM, nonce-prefixed and
// base64-encoded. An empty plain stays empty: "no password set" must not
// become a ciphertext of the empty string, since keepStoredPassword and
// AdminAccountPasswordSet both depend on "" meaning "none".
func encryptPassword(key []byte, plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("admin password cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("admin password gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("admin password nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// decryptPassword reverses encryptPassword. A value with no encPrefix is a
// legacy plaintext row and is returned unchanged.
func decryptPassword(key []byte, stored string) (string, error) {
	if stored == "" || !strings.HasPrefix(stored, encPrefix) {
		return stored, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, encPrefix))
	if err != nil {
		return "", fmt.Errorf("admin password decode: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("admin password cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("admin password gcm: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("admin password ciphertext too short")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("admin password open: %w", err)
	}
	return string(plain), nil
}

// The managed local-administrator password is the one secret a policy holds.
// The agent needs it, so it travels in the policy dispatch over the agent's
// authenticated socket. Nobody else does: every response to the console
// carries AdminAccountPasswordSet instead, and the per-device state rows never
// store it.
//
// It used to be returned in full by every read endpoint, to any logged-in role
// including viewer, and copied into device_system_policy_states once per
// device by the agent's report.

// redacted returns the settings with the password replaced by a flag.
func (s PolicySettings) redacted() PolicySettings {
	s.AdminAccountPasswordSet = s.AdminAccountPassword != ""
	s.AdminAccountPassword = ""
	return s
}

// redacted returns the policy's settings with the password replaced by a flag.
func (c PolicyConfig) redacted() PolicyConfig {
	c.AdminAccountPasswordSet = c.AdminAccountPassword != ""
	c.AdminAccountPassword = ""
	return c
}

func redactPolicy(p *SystemPolicy) *SystemPolicy {
	if p == nil {
		return nil
	}
	c := *p
	c.Settings = p.Settings.redacted()
	return &c
}

func redactPolicies(ps []SystemPolicy) []SystemPolicy {
	out := make([]SystemPolicy, len(ps))
	for i := range ps {
		out[i] = *redactPolicy(&ps[i])
	}
	return out
}

func redactCompiled(c *CompiledPolicy) *CompiledPolicy {
	if c == nil {
		return nil
	}
	cp := *c
	cp.Settings = c.Settings.redacted()
	return &cp
}

func redactState(s *DeviceSystemPolicyState) *DeviceSystemPolicyState {
	if s == nil {
		return nil
	}
	c := *s
	c.AppliedSettings = s.AppliedSettings.redacted()
	return &c
}

func redactStates(ss []DeviceSystemPolicyState) []DeviceSystemPolicyState {
	out := make([]DeviceSystemPolicyState, len(ss))
	for i := range ss {
		out[i] = *redactState(&ss[i])
	}
	return out
}

// keepStoredPassword applies the edit rule for the password: a blank password
// in an update means "unchanged" (the console never receives the stored one, so
// it cannot send it back), and a policy that does not turn the managed account
// on holds no password.
func keepStoredPassword(incoming *PolicyConfig, stored PolicyConfig) {
	incoming.AdminAccountPasswordSet = false
	if incoming.AdminAccountEnabled == nil || !*incoming.AdminAccountEnabled {
		incoming.AdminAccountPassword = ""
		return
	}
	if incoming.AdminAccountPassword == "" {
		incoming.AdminAccountPassword = stored.AdminAccountPassword
	}
}

// ReencryptLegacyPasswords finds system_policies rows whose admin password is
// still in the plaintext form a server before this encryption existed wrote,
// and rewrites them through encryptPassword. Without this, a policy that was
// never edited again after that version keeps a plaintext password in
// settings_json indefinitely -- decryptPassword reads it fine, but reading
// never rewrites it, only Create/Update do. Mirrors ScrubStoredStatePasswords
// for the sibling device_system_policy_states table. Runs once at startup;
// rows already encrypted (or with no password at all) are left untouched.
func (r *Repository) ReencryptLegacyPasswords(ctx context.Context) {
	var rows []struct {
		ID           string `db:"id"`
		SettingsJSON string `db:"settings_json"`
	}
	q := `SELECT id, settings_json FROM system_policies
	      WHERE settings_json LIKE '%admin_account_password"%' AND settings_json NOT LIKE '%` + encPrefix + `%'`
	if err := r.db.SelectContext(ctx, &rows, r.db.Rebind(q)); err != nil {
		log.Warn().Err(err).Msg("system policy: could not scan policies for legacy plaintext passwords")
		return
	}
	upgraded := 0
	for _, row := range rows {
		cfg, err := decodePolicyConfig([]byte(row.SettingsJSON))
		if err != nil || cfg.AdminAccountPassword == "" {
			continue
		}
		enc, err := encryptPassword(r.encKey, cfg.AdminAccountPassword)
		if err != nil {
			log.Warn().Err(err).Str("policy", row.ID).Msg("system policy: could not encrypt legacy password")
			continue
		}
		cfg.AdminAccountPassword = enc
		cfg.SettingsVersion = settingsVersion
		clean, err := json.Marshal(cfg)
		if err != nil {
			continue
		}
		if _, err := r.db.ExecContext(ctx, r.db.Rebind(
			`UPDATE system_policies SET settings_json = ? WHERE id = ?`),
			string(clean), row.ID); err != nil {
			log.Warn().Err(err).Str("policy", row.ID).Msg("system policy: re-encrypt legacy password")
			continue
		}
		upgraded++
	}
	if upgraded > 0 {
		log.Info().Int("rows", upgraded).Msg("system policy: encrypted managed admin passwords a prior server version stored in plaintext")
	}
}

// ScrubStoredStatePasswords removes passwords that earlier versions stored in
// device_system_policy_states. It runs once at startup; rows already clean are
// not touched.
func (r *Repository) ScrubStoredStatePasswords(ctx context.Context) {
	var rows []struct {
		DeviceID string `db:"device_id"`
		JSON     string `db:"applied_settings_json"`
	}
	q := `SELECT device_id, applied_settings_json FROM device_system_policy_states
	      WHERE applied_settings_json LIKE '%admin_account_password"%'`
	if err := r.db.SelectContext(ctx, &rows, r.db.Rebind(q)); err != nil {
		log.Warn().Err(err).Msg("system policy: could not scan state rows for stored passwords")
		return
	}
	scrubbed := 0
	for _, row := range rows {
		var s PolicySettings
		if err := json.Unmarshal([]byte(row.JSON), &s); err != nil || s.AdminAccountPassword == "" {
			continue
		}
		clean, err := json.Marshal(s.redacted())
		if err != nil {
			continue
		}
		if _, err := r.db.ExecContext(ctx, r.db.Rebind(
			`UPDATE device_system_policy_states SET applied_settings_json = ? WHERE device_id = ?`),
			string(clean), row.DeviceID); err != nil {
			log.Warn().Err(err).Str("device", row.DeviceID).Msg("system policy: scrub stored password")
			continue
		}
		scrubbed++
	}
	if scrubbed > 0 {
		log.Info().Int("rows", scrubbed).Msg("system policy: removed managed admin passwords stored in device state rows")
	}
}
