package systempolicy

import (
	"context"
	"encoding/json"

	"github.com/rs/zerolog/log"
)

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
// it cannot send it back), and turning the managed account off clears it.
func keepStoredPassword(incoming *PolicySettings, stored PolicySettings) {
	incoming.AdminAccountPasswordSet = false
	if !incoming.AdminAccountEnabled {
		incoming.AdminAccountPassword = ""
		return
	}
	if incoming.AdminAccountPassword == "" {
		incoming.AdminAccountPassword = stored.AdminAccountPassword
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
