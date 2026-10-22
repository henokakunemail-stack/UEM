package networkfilter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	filterStateFile = "filter-state.json"
	dnsBackupFile   = "dns-backup.json"
)

// filterState is the last policy this agent applied, kept so a restart puts
// it back. The agent used to keep nothing: whitelist mode lived in memory, was
// torn down on shutdown, and the server did not re-send it on reconnect
// because its row still read "synced" at the same version.
type filterState struct {
	Version string   `json:"version"`
	Mode    string   `json:"mode"`
	Blocked []string `json:"blocked,omitempty"`
	Allowed []string `json:"allowed,omitempty"`
}

// WithStateDir enables persistence under dir (the agent's creds directory).
func (e *Engine) WithStateDir(dir string) *Engine {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stateDir = dir
	return e
}

func (e *Engine) statePath(name string) string { return filepath.Join(e.stateDir, name) }

func (e *Engine) writeState(name string, v any) error {
	if e.stateDir == "" {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(e.stateDir, 0o700); err != nil {
		return err
	}
	return writeAtomic(e.statePath(name), b, 0o600)
}

func (e *Engine) readState(name string, v any) (bool, error) {
	if e.stateDir == "" {
		return false, nil
	}
	b, err := os.ReadFile(e.statePath(name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(b, v)
}

func (e *Engine) saveDNSBackup(b dnsBackup) error { return e.writeState(dnsBackupFile, b) }

func (e *Engine) removeDNSBackup() {
	if e.stateDir == "" {
		return
	}
	if err := os.Remove(e.statePath(dnsBackupFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn().Err(err).Msg("remove DNS backup")
	}
}

// ApplyPolicy applies a policy from the server and remembers it for the next
// start.
func (e *Engine) ApplyPolicy(version, mode string, blocked, allowed []string) (int, string, error) {
	count, degraded, err := e.ApplyFilter(mode, blocked, allowed)
	if err == nil {
		e.mu.Lock()
		werr := e.writeState(filterStateFile, filterState{Version: version, Mode: mode, Blocked: blocked, Allowed: allowed})
		e.mu.Unlock()
		if werr != nil {
			log.Warn().Err(werr).Msg("persist filter state; a restart will wait for the server to re-send the policy")
		}
	}
	return count, degraded, err
}

// RecoveredPolicy is what Recover re-applied, for the caller to report.
type RecoveredPolicy struct {
	Version  string
	Count    int
	Degraded string
	Err      error
}

// Recover runs once at startup, before the agent connects. A DNS backup still
// on disk means the previous run ended without restoring DNS (a crash or a
// kill): if that run was enforcing a whitelist, the adapters are left on
// 127.0.0.1 and the persisted backup is adopted, so the resolver comes back on
// top of them; otherwise DNS is restored from it. Then the last applied policy
// is applied again. It returns nil when there was nothing to re-apply.
func (e *Engine) Recover() *RecoveredPolicy {
	e.mu.Lock()
	var state filterState
	haveState, err := e.readState(filterStateFile, &state)
	if err != nil {
		log.Warn().Err(err).Msg("read persisted filter state")
		haveState = false
	}
	var backup dnsBackup
	haveBackup, err := e.readState(dnsBackupFile, &backup)
	if err != nil {
		log.Warn().Err(err).Msg("read persisted DNS backup")
		haveBackup = false
	}
	if haveBackup {
		if haveState && state.Mode == "whitelist" && len(state.Allowed) > 0 {
			e.activeBackup = &backup
		} else {
			if err := e.dns.Restore(backup); err != nil {
				log.Warn().Err(err).Msg("restore DNS left behind by the previous run")
			}
			e.removeDNSBackup()
		}
	}
	e.mu.Unlock()

	if !haveState {
		return nil
	}
	count, degraded, err := e.ApplyFilter(state.Mode, state.Blocked, state.Allowed)
	log.Info().Str("version", state.Version).Str("mode", state.Mode).Int("rules", count).
		Msg("re-applied the persisted filter policy at startup")
	return &RecoveredPolicy{Version: state.Version, Count: count, Degraded: degraded, Err: err}
}

// expiringFirewall is a backend whose entries lapse on their own (nftables set
// elements carry a timeout of resolveCacheTTL).
type expiringFirewall interface {
	EntriesExpire() bool
}

// RunRefresh re-installs an expiring blacklist firewall layer until ctx ends.
// On Linux every blocked address used to drop out after an hour, silently,
// while the device kept reporting the policy as enforced.
func (e *Engine) RunRefresh(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := e.refreshOnce(); err != nil {
				log.Warn().Err(err).Msg("refresh filter firewall layer")
			}
		}
	}
}

func (e *Engine) refreshOnce() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	exp, ok := e.firewall.(expiringFirewall)
	if !ok || !exp.EntriesExpire() || e.currentMode != "blacklist" || len(e.lastBlocked) == 0 {
		return nil
	}
	_, _, err := applyFirewall(e.firewall, e.resolver, e.lastBlocked)
	if err != nil {
		return fmt.Errorf("re-apply blacklist firewall: %w", err)
	}
	return nil
}
