package agentupdate

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
)

// A release package carries the agents built with the server in agents/ next
// to the server binary (agent-windows-amd64.exe, agent-linux-arm64, ...). The
// one-line installer already hands those out, but an agent that is already
// enrolled could only be moved onto them if an operator downloaded each file
// from the release page and uploaded it again, typing the version by hand.
// These endpoints let the console list what the package carries and register
// it as releases in one step, with the version and platform read out of the
// binaries themselves rather than retyped.

// bundledName matches the file names the release packages use.
var bundledName = regexp.MustCompile(`^agent-(windows|linux|darwin|macos)-(amd64|arm64|386)(\.exe)?$`)

// stampedVersion finds the version the release build stamps into the agent.
var stampedVersion = regexp.MustCompile(`osinfo\.Version=([^\s"']+)`)

// binaryFacts is what a Go binary records about its own build.
type binaryFacts struct {
	IsGo    bool
	OSName  string
	Arch    string
	Version string
}

// inspectBinary reads the build information the Go toolchain embeds in every
// binary it links, whatever the target platform: GOOS, GOARCH, and the
// -ldflags the release build used to stamp osinfo.Version. A file that is not
// a Go binary reports IsGo false and nothing else.
func inspectBinary(path string) binaryFacts {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return binaryFacts{}
	}
	f := binaryFacts{IsGo: true}
	for _, s := range info.Settings {
		switch s.Key {
		case "GOOS":
			f.OSName = NormalizeOSName(s.Value)
		case "GOARCH":
			f.Arch = normalizeArch(s.Value)
		case "-ldflags":
			if m := stampedVersion.FindStringSubmatch(s.Value); m != nil {
				f.Version = strings.TrimPrefix(m[1], "v")
			}
		}
	}
	return f
}

// BundledAgent is one agent binary found in the server's release package.
type BundledAgent struct {
	File       string `json:"file"`
	OSName     string `json:"os_name"`
	Arch       string `json:"arch"`
	Version    string `json:"version"`
	FileSize   int64  `json:"file_size"`
	Importable bool   `json:"importable"`
	Reason     string `json:"reason"`
	ReleaseID  string `json:"release_id"`
}

// WithBundledAgents names the directories a release package unpacks its agents
// into, searched in order, and the server's own version, which is the version
// of an agent that was built without one stamped.
func (h *Handler) WithBundledAgents(serverVersion string, dirs ...string) *Handler {
	h.serverVersion = strings.TrimPrefix(serverVersion, "v")
	h.bundleDirs = nil
	for _, d := range dirs {
		if d != "" {
			h.bundleDirs = append(h.bundleDirs, d)
		}
	}
	return h
}

// scanBundled lists the agent binaries in the bundle directories. The first
// directory that has a file wins, matching the installer's lookup order.
func (h *Handler) scanBundled(r *http.Request) ([]BundledAgent, map[string]string) {
	out := []BundledAgent{}
	paths := map[string]string{}
	for _, dir := range h.bundleDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, ent := range entries {
			m := bundledName.FindStringSubmatch(ent.Name())
			if m == nil || ent.IsDir() || paths[ent.Name()] != "" {
				continue
			}
			full := filepath.Join(dir, ent.Name())
			st, err := os.Stat(full)
			if err != nil || !st.Mode().IsRegular() {
				continue
			}
			paths[ent.Name()] = full
			out = append(out, h.describeBundled(r, ent.Name(), full, m[1], m[2], st.Size()))
		}
	}
	return out, paths
}

func (h *Handler) describeBundled(r *http.Request, name, full, osName, arch string, size int64) BundledAgent {
	b := BundledAgent{File: name, OSName: NormalizeOSName(osName), Arch: normalizeArch(arch), FileSize: size}
	facts := inspectBinary(full)
	switch {
	case !facts.IsGo:
		b.Reason = "not an agent build: the file carries no Go build information"
		return b
	case facts.OSName != b.OSName || facts.Arch != b.Arch:
		b.Reason = fmt.Sprintf("file name says %s/%s but the binary is built for %s/%s",
			b.OSName, b.Arch, facts.OSName, facts.Arch)
		return b
	}
	b.Version = facts.Version
	if b.Version == "" && h.serverVersion != "" && h.serverVersion != "dev" {
		b.Version = h.serverVersion
	}
	if b.Version == "" {
		b.Reason = "the binary has no version stamped and this server build has none to lend it"
		return b
	}
	if rel, err := h.repo.GetActiveReleaseForDevice(r.Context(), b.Version, b.OSName, b.Arch); err == nil {
		b.ReleaseID = rel.ID
		b.Reason = "already registered as a release"
		return b
	}
	b.Importable = true
	return b
}

func (h *Handler) handleListBundled(w http.ResponseWriter, r *http.Request) {
	list, _ := h.scanBundled(r)
	writeJSON(w, http.StatusOK, list)
}

// handleImportBundled registers bundled agents as releases. The body names the
// files to import; an empty list imports everything importable. Only names the
// scan produced are accepted, so the request cannot point at a path.
func (h *Handler) handleImportBundled(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Files []string `json:"files"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
	}

	list, paths := h.scanBundled(r)
	want := map[string]bool{}
	for _, f := range req.Files {
		want[f] = true
	}

	actorID := auth.UserIDFromContext(r.Context())
	imported := []AgentReleaseDTO{}
	skipped := []BundledAgent{}
	for _, b := range list {
		if len(want) > 0 && !want[b.File] {
			continue
		}
		delete(want, b.File)
		if !b.Importable {
			skipped = append(skipped, b)
			continue
		}
		rel, err := h.importBundled(r, b, paths[b.File], actorID)
		if err != nil {
			b.Importable = false
			b.Reason = err.Error()
			skipped = append(skipped, b)
			continue
		}
		imported = append(imported, rel.toDTO())
	}
	for name := range want {
		skipped = append(skipped, BundledAgent{File: name, Reason: "not found in the server's agent bundle"})
	}

	writeJSON(w, http.StatusOK, map[string]any{"imported": imported, "skipped": skipped})
}

// importBundled copies one bundled binary into release storage and records it.
// It is copied rather than referenced so that the release keeps serving the
// exact bytes it was checksummed from after the next server update replaces
// the bundle.
func (h *Handler) importBundled(r *http.Request, b BundledAgent, src, actorID string) (*AgentRelease, error) {
	destPath, err := releasePath(h.storageDir, b.Version, b.OSName, b.Arch, b.File)
	if err != nil {
		return nil, err
	}
	in, err := os.Open(src)
	if err != nil {
		return nil, fmt.Errorf("open bundled agent: %w", err)
	}
	defer in.Close()
	out, err := os.Create(destPath)
	if err != nil {
		return nil, fmt.Errorf("create release file: %w", err)
	}
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(out, hasher), in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(destPath)
		return nil, fmt.Errorf("copy bundled agent: %w", err)
	}

	rel := &AgentRelease{
		ID:             newID(),
		Version:        b.Version,
		OSName:         b.OSName,
		Arch:           b.Arch,
		FilePath:       destPath,
		FileSize:       written,
		SHA256Checksum: hex.EncodeToString(hasher.Sum(nil)),
		Changelog:      "Bundled with server " + h.serverVersion,
		IsActive:       true,
		UploadedBy:     actorID,
	}
	if err := h.repo.CreateRelease(r.Context(), rel); err != nil {
		_ = os.Remove(destPath)
		return nil, err
	}
	_ = h.auditor.Log(r.Context(), "user", actorID, "agent_update.release_import", rel.ID, map[string]string{
		"version":  rel.Version,
		"os_name":  rel.OSName,
		"arch":     rel.Arch,
		"checksum": rel.SHA256Checksum,
		"source":   b.File,
	})
	return rel, nil
}
