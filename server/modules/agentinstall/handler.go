// Package agentinstall serves the one-line Linux agent installer:
//
//	curl -fsSL https://SERVER/install/linux.sh | sudo bash -s -- --token TOKEN
//
// The script is public; it holds nothing but the steps. The binary it fetches
// is handed out only against an enrollment token that would be accepted right
// now, so the server is not an open download mirror for anyone who finds it.
package agentinstall

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

//go:embed install-linux.sh
var linuxScript []byte

// TokenChecker validates an enrollment token without consuming it.
type TokenChecker interface {
	EnrollmentTokenValid(ctx context.Context, plaintext string) (bool, error)
}

// ReleaseFinder returns the file of the newest active agent release for an OS
// and architecture, or an error when there is none.
type ReleaseFinder func(ctx context.Context, osName, arch string) (path string, err error)

// Handler serves the installer script and the agent binary.
type Handler struct {
	tokens     TokenChecker
	releases   ReleaseFinder
	bundleDirs []string

	mu     sync.Mutex
	hashes map[string]cachedHash
}

type cachedHash struct {
	size    int64
	modTime time.Time
	sum     string
}

// NewHandler builds the installer handler. bundleDirs hold agent binaries
// shipped with the server (agent-linux-amd64, agent-linux-arm64), searched in
// order; they are the fallback when no agent release has been uploaded in
// Agent Updates.
func NewHandler(tokens TokenChecker, releases ReleaseFinder, bundleDirs ...string) *Handler {
	return &Handler{tokens: tokens, releases: releases, bundleDirs: bundleDirs, hashes: map[string]cachedHash{}}
}

// Register mounts the public installer routes.
func (h *Handler) Register(r chi.Router) {
	r.Get("/install/linux.sh", h.serveScript)
	r.Get("/install/agent/{os}/{arch}", h.serveBinary)
}

func (h *Handler) serveScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(linuxScript)
}

var supportedArch = map[string]bool{"amd64": true, "arm64": true}

func (h *Handler) serveBinary(w http.ResponseWriter, r *http.Request) {
	osName, arch := chi.URLParam(r, "os"), chi.URLParam(r, "arch")
	if osName != "linux" || !supportedArch[arch] {
		plainError(w, http.StatusNotFound, fmt.Sprintf("no agent installer for %s/%s; supported: linux/amd64, linux/arm64", osName, arch))
		return
	}

	token := r.Header.Get("X-Enrollment-Token")
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	ok, err := h.tokens.EnrollmentTokenValid(r.Context(), token)
	if err != nil {
		log.Error().Err(err).Msg("agent installer: token check")
		plainError(w, http.StatusInternalServerError, "could not check the enrollment token")
		return
	}
	if !ok {
		plainError(w, http.StatusForbidden, "the enrollment token is missing, expired, revoked or used up; create a new one in the console")
		return
	}

	path, source := h.locate(r.Context(), osName, arch)
	if path == "" {
		plainError(w, http.StatusNotFound, fmt.Sprintf(
			"no agent binary for %s/%s on this server. Upload one in Agent Updates, or place agent-%s-%s in %s",
			osName, arch, osName, arch, strings.Join(h.bundleDirs, " or ")))
		return
	}
	sum, err := h.sha256(path)
	if err != nil {
		log.Error().Err(err).Str("path", path).Msg("agent installer: hash binary")
		plainError(w, http.StatusInternalServerError, "could not read the agent binary")
		return
	}
	w.Header().Set("X-Agent-SHA256", sum)
	w.Header().Set("X-Agent-Source", source)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="agent-%s-%s"`, osName, arch))
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, path)
}

// locate prefers an uploaded release, so an operator who publishes a new agent
// in Agent Updates installs that one everywhere, and falls back to the binary
// bundled with the server.
func (h *Handler) locate(ctx context.Context, osName, arch string) (path, source string) {
	if h.releases != nil {
		if p, err := h.releases(ctx, osName, arch); err == nil && regularFile(p) {
			return p, "release"
		}
	}
	for _, dir := range h.bundleDirs {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, fmt.Sprintf("agent-%s-%s", osName, arch))
		if regularFile(p) {
			return p, "bundle"
		}
	}
	return "", ""
}

func regularFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

// sha256 hashes the binary once per (size, mtime), not on every download.
func (h *Handler) sha256(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	h.mu.Lock()
	c, ok := h.hashes[path]
	h.mu.Unlock()
	if ok && c.size == fi.Size() && c.modTime.Equal(fi.ModTime()) {
		return c.sum, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	h.mu.Lock()
	h.hashes[path] = cachedHash{size: fi.Size(), modTime: fi.ModTime(), sum: sum}
	h.mu.Unlock()
	return sum, nil
}

// plainError answers in text, because the reader is curl in a shell script
// that prints the body as the reason.
func plainError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, strings.TrimSpace(msg)+"\n")
}
