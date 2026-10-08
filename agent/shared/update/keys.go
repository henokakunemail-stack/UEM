package update

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
)

// EmbeddedPublicKey is the base64 Ed25519 public key this build was stamped
// with, if any:
//
//	go build -ldflags "-X .../agent/shared/update.EmbeddedPublicKey=<base64>"
//
// It is a var rather than const for the same reason osinfo.Version is: a build
// that was not stamped still has to compile and run, and the empty value is the
// signal that this binary has no opinion about who signs its updates.
//
// A key baked into the binary is the strong option -- the agent cannot be
// talked into trusting a different key by anything it talks to. A build with
// none falls back to fetching /api/agent/config, which is weaker: it trusts the
// server for the key that is supposed to constrain the server. That is a
// deployment trade-off, and it is the deployment's to make.
var EmbeddedPublicKey = ""

// parsePublicKey decodes the operator's base64 Ed25519 public key. A key of the
// wrong length would decode to a zero key that silently verifies nothing, and
// padding noise is the most likely typo, so both are reported by name rather
// than folded into one "invalid key".
func parsePublicKey(b64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// loadTrustedKey resolves the key this agent verifies release manifests with:
// the one it was built with, else the one the server publishes. Returns nil
// when neither is available, which ApplyUpdate treats as "checksum only" --
// a fleet that has not rolled out signing yet still has to be able to update.
func (e *Engine) loadTrustedKey(ctx context.Context) ed25519.PublicKey {
	if EmbeddedPublicKey != "" {
		key, err := parsePublicKey(EmbeddedPublicKey)
		if err != nil {
			// A build that was stamped with a key it cannot parse has a broken
			// release pipeline, and falling back to the server here would hide
			// it behind a working-looking update. Fail the key, keep updating.
			log.Error().Err(err).Msg("embedded release-signing key is unusable")
			return nil
		}
		return key
	}

	cfg, err := e.fetchAgentConfig(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("fetch release signing config; updates fall back to checksum-only")
		return nil
	}
	if cfg.PublicKey == "" {
		return nil
	}
	key, err := parsePublicKey(cfg.PublicKey)
	if err != nil {
		log.Warn().Err(err).Msg("server publishes an unusable release-signing key")
		return nil
	}
	return key
}

type agentConfig struct {
	PublicKey string `json:"public_key"`
}

// fetchAgentConfig reads the public signing key the server publishes. It is a
// plain GET with no device credentials: the endpoint carries only public
// material and requires none.
func (e *Engine) fetchAgentConfig(ctx context.Context) (agentConfig, error) {
	var out agentConfig
	reqCtx, cancel := context.WithTimeout(ctx, oneRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, e.serverURL+"/api/agent/config", nil)
	if err != nil {
		return out, err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return out, fmt.Errorf("get agent config: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("agent config returned status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("decode agent config: %w", err)
	}
	return out, nil
}

// InitTrust resolves the release-signing key once, before any update runs, so
// the resolution cost and its failure modes belong to startup rather than to
// the first update an operator dispatches at 3am. A failure here is a warning,
// not fatal: an agent without a trusted key still installs releases and reports
// on them, it just cannot attest them.
func (e *Engine) InitTrust(ctx context.Context, currentVersion string) {
	if key := e.loadTrustedKey(ctx); key != nil {
		e.publicKey = key
		log.Info().Msg("release manifest verification is enabled")
	}
	if currentVersion != "" {
		e.currentVersion = currentVersion
	}
}

// oneRequestTimeout bounds the startup config fetch so a server that is up but
// slow cannot hold agent startup indefinitely.
const oneRequestTimeout = 10 * time.Second
