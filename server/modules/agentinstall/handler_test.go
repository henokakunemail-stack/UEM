package agentinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type tokens map[string]bool

func (t tokens) EnrollmentTokenValid(_ context.Context, p string) (bool, error) { return t[p], nil }

func server(t *testing.T, releasePath, bundleDir string) *httptest.Server {
	t.Helper()
	finder := func(context.Context, string, string) (string, error) {
		if releasePath == "" {
			return "", errors.New("none")
		}
		return releasePath, nil
	}
	r := chi.NewRouter()
	NewHandler(tokens{"good": true}, finder, bundleDir).Register(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url, token string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("X-Enrollment-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScriptIsPublic(t *testing.T) {
	srv := server(t, "", "")
	resp, body := get(t, srv.URL+"/install/linux.sh", "")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(body, "#!/usr/bin/env bash") {
		t.Fatalf("status %d, body starts %q", resp.StatusCode, body[:min(len(body), 40)])
	}
}

// The binary is handed out only against a token that would enroll right now.
func TestBinaryNeedsAValidEnrollmentToken(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "agent-linux-amd64", "ELF-bundle")
	srv := server(t, "", dir)
	for _, tok := range []string{"", "wrong"} {
		resp, body := get(t, srv.URL+"/install/agent/linux/amd64", tok)
		if resp.StatusCode != http.StatusForbidden || strings.Contains(body, "ELF") {
			t.Fatalf("token %q: status %d body %q, want 403 without the binary", tok, resp.StatusCode, body)
		}
	}
}

func TestBinaryComesWithItsChecksum(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "agent-linux-arm64", "ELF-arm64")
	srv := server(t, "", dir)
	resp, body := get(t, srv.URL+"/install/agent/linux/arm64", "good")
	if resp.StatusCode != http.StatusOK || body != "ELF-arm64" {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	sum := sha256.Sum256([]byte("ELF-arm64"))
	if got := resp.Header.Get("X-Agent-SHA256"); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("X-Agent-SHA256 = %q, want the file's hash", got)
	}
	if resp.Header.Get("X-Agent-Source") != "bundle" {
		t.Fatalf("source = %q, want bundle", resp.Header.Get("X-Agent-Source"))
	}
}

// A release uploaded in Agent Updates wins over the bundled binary, so a
// published fix reaches new installs too.
func TestUploadedReleaseIsPreferredOverTheBundle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "agent-linux-amd64", "ELF-bundle")
	rel := writeFile(t, t.TempDir(), "release.bin", "ELF-release")
	srv := server(t, rel, dir)
	resp, body := get(t, srv.URL+"/install/agent/linux/amd64", "good")
	if body != "ELF-release" || resp.Header.Get("X-Agent-Source") != "release" {
		t.Fatalf("served %q from %q, want the release", body, resp.Header.Get("X-Agent-Source"))
	}
}

func TestMissingBinaryAndUnsupportedPlatformsExplainThemselves(t *testing.T) {
	srv := server(t, "", t.TempDir())
	resp, body := get(t, srv.URL+"/install/agent/linux/amd64", "good")
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "Agent Updates") {
		t.Fatalf("status %d body %q, want 404 naming Agent Updates", resp.StatusCode, body)
	}
	for _, p := range []string{"/install/agent/windows/amd64", "/install/agent/linux/386", "/install/agent/linux/..%2f..%2fetc"} {
		resp, _ := get(t, srv.URL+p, "good")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, resp.StatusCode)
		}
	}
}
