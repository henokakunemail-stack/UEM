package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/transport"
)

// trickleServer serves content in chunks with a pause between them, the way a
// slow link delivers a release, and records the progress reports it is sent.
// After the first stallAfter chunks it stops sending until the client leaves.
type trickleServer struct {
	blobServer
	chunks     int
	pause      time.Duration
	stallAfter int
}

func (s *trickleServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.mu.Lock()
			s.reports = append(s.reports, body["status"])
			s.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		flusher := w.(http.Flusher)
		size := (len(s.content) + s.chunks - 1) / s.chunks
		for i := 0; i < len(s.content); i += size {
			if s.stallAfter > 0 && i/size == s.stallAfter {
				<-r.Context().Done()
				return
			}
			end := min(i+size, len(s.content))
			_, _ = w.Write([]byte(s.content[i:end]))
			flusher.Flush()
			time.Sleep(s.pause)
		}
	}
}

func applyFrom(t *testing.T, srv *trickleServer, configure func(*Engine)) error {
	t.Helper()
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	execPath := filepath.Join(t.TempDir(), "agent.exe")
	if err := os.WriteFile(execPath, []byte("the running agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(srv.content))
	e := NewEngine(ts.URL, "dev-1", "secret")
	e.SetExecutablePath(execPath)
	configure(e)
	return e.ApplyUpdate(context.Background(), UpdateParams{
		TaskID:         "task-slow",
		TargetVersion:  "2.0.0",
		DownloadURL:    "/blob",
		SHA256Checksum: hex.EncodeToString(sum[:]),
	})
}

// The release was downloaded with the client the agent uses for its small API
// calls, whose 60 second limit covers reading the whole body. A PC on a link
// slower than about 1.5 Mbps could not fetch an 11 MB agent in that time, so
// its update failed with "context deadline exceeded (Client.Timeout ... while
// reading body)" every time it was retried, although data was arriving the
// whole while. Here the API client's limit is shorter than the transfer, which
// is the same situation in miniature: the download must still complete.
func TestASlowButSteadyDownloadIsNotCutOff(t *testing.T) {
	srv := &trickleServer{
		blobServer: blobServer{content: strings.Repeat("agent release bytes ", 500)},
		chunks:     20,
		pause:      50 * time.Millisecond,
	}
	_ = applyFrom(t, srv, func(e *Engine) {
		e.client = transport.NewHTTPClient(300 * time.Millisecond)
		e.stallTimeout = 500 * time.Millisecond
	})
	// "verifying" is reported once the whole body has been read. What happens
	// after that (this content is not a program, so the swap is rolled back)
	// is covered by the verify tests, not this one.
	if !srv.saw(t, "verifying") {
		t.Fatalf("the download never completed (reports: %v): a transfer that was making "+
			"progress was cut off by a fixed time limit", srv.all())
	}
}

// A download that stops receiving data must still end, and say why, rather
// than hold the update for the full 30 minute ceiling.
func TestAStalledDownloadIsAbandoned(t *testing.T) {
	srv := &trickleServer{
		blobServer: blobServer{content: strings.Repeat("agent release bytes ", 500)},
		chunks:     20,
		pause:      10 * time.Millisecond,
		stallAfter: 3,
	}
	start := time.Now()
	err := applyFrom(t, srv, func(e *Engine) { e.stallTimeout = 300 * time.Millisecond })
	if err == nil {
		t.Fatal("ApplyUpdate succeeded although the download stopped half way")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("a stalled download took %s to give up, want about the stall timeout", elapsed)
	}
	if !strings.Contains(err.Error(), "stalled") {
		t.Errorf("error = %q, want it to say the download stalled", err)
	}
	if !srv.saw(t, "failed") {
		t.Errorf("reports = %v, want the failure reported to the server", srv.all())
	}
}
