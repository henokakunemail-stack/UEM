package agentupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/update"
	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

func setupTestServer(t *testing.T, pubKey ed25519.PublicKey) (*Handler, *Repository, ed25519.PrivateKey, *chi.Mux) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	var privKey ed25519.PrivateKey
	var pubB64 string
	if pubKey != nil {
		pubB64 = base64.StdEncoding.EncodeToString(pubKey)
	} else {
		var err error
		pubKey, privKey, err = ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		pubB64 = base64.StdEncoding.EncodeToString(pubKey)
	}

	repo := NewRepository(database)
	h := NewHandler(repo, nil, devicemgmt.NewRepository(database), discardAuditor{},
		t.TempDir(), func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(rbac.WithRole(r.Context(), rbac.RoleAdmin)))
			})
		}, "").WithSigningPublicKey(pubB64, "")

	router := chi.NewRouter()
	h.Register(router)

	return h, repo, privKey, router
}

func uploadReleaseFixture(t *testing.T, router *chi.Mux, version, osName, arch string, payload []byte) string {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	_ = writer.WriteField("version", version)
	_ = writer.WriteField("os_name", osName)
	_ = writer.WriteField("arch", arch)
	_ = writer.WriteField("changelog", "test release")
	fw, err := writer.CreateFormFile("file", "update.bin")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	_, _ = fw.Write(payload)
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/agent-updates/releases", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("upload failed with code %d: %s", rec.Code, rec.Body.String())
	}

	var rel AgentReleaseDTO
	if err := json.NewDecoder(rec.Body).Decode(&rel); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	return rel.ID
}

func TestSignReleaseEndpoint(t *testing.T) {
	pubKey, privKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}

	h, repo, _, router := setupTestServer(t, pubKey)
	_ = h

	binContent := []byte("binary-payload-v1.0.0")
	relID := uploadReleaseFixture(t, router, "1.0.0", "windows", "amd64", binContent)

	rel, err := repo.GetRelease(context.Background(), relID)
	if err != nil {
		t.Fatalf("get release: %v", err)
	}

	publishedAt := time.Now().UTC()
	downloadURL := releaseDownloadURL(rel)

	manifest := manifestForRelease(rel, downloadURL, publishedAt)
	manifest.MinimumSupportedVersion = "0.9.0"
	sig, err := manifest.Sign(privKey)
	if err != nil {
		t.Fatalf("sign manifest: %v", err)
	}

	// 1. Sign release with valid signature
	signBody, _ := json.Marshal(map[string]string{
		"signature":                 sig,
		"minimum_supported_version": "0.9.0",
		"download_url":              downloadURL,
		"published_at":              publishedAt.Format(time.RFC3339),
	})

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/agent-updates/releases/%s/sign", relID), bytes.NewReader(signBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("sign endpoint returned %d: %s", rec.Code, rec.Body.String())
	}

	// Verify database state updated
	updatedRel, err := repo.GetRelease(context.Background(), relID)
	if err != nil {
		t.Fatalf("get updated release: %v", err)
	}
	if updatedRel.Ed25519Signature != sig {
		t.Errorf("expected signature %s, got %s", sig, updatedRel.Ed25519Signature)
	}
	if updatedRel.MinimumSupportedVersion != "0.9.0" {
		t.Errorf("expected minimum_supported_version 0.9.0, got %s", updatedRel.MinimumSupportedVersion)
	}

	// 2. Bad signature rejection
	badSignBody, _ := json.Marshal(map[string]string{
		"signature":                 base64.StdEncoding.EncodeToString(make([]byte, 64)),
		"minimum_supported_version": "0.9.0",
		"download_url":              downloadURL,
		"published_at":              publishedAt.Format(time.RFC3339),
	})
	reqBad := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/agent-updates/releases/%s/sign", relID), bytes.NewReader(badSignBody))
	reqBad.Header.Set("Content-Type", "application/json")
	recBad := httptest.NewRecorder()
	router.ServeHTTP(recBad, reqBad)

	if recBad.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for invalid signature, got %d: %s", recBad.Code, recBad.Body.String())
	}
}

func TestAgentConfigEndpoint(t *testing.T) {
	pubKey, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	_, _, _, router := setupTestServer(t, pubKey)

	req := httptest.NewRequest(http.MethodGet, "/api/agent/config", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("config endpoint returned %d", rec.Code)
	}

	var res map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatalf("decode config response: %v", err)
	}

	if res["public_key"] != base64.StdEncoding.EncodeToString(pubKey) {
		t.Errorf("expected public_key %s, got %v", base64.StdEncoding.EncodeToString(pubKey), res["public_key"])
	}
}

func TestAgentEngineManifestVerification(t *testing.T) {
	pubKey, privKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// Dummy server returning valid executable file bytes for runtime.GOOS
	validPEBinary := make([]byte, 0x100)
	if runtime.GOOS == "windows" {
		validPEBinary[0], validPEBinary[1] = 'M', 'Z'
		binary.LittleEndian.PutUint32(validPEBinary[0x3c:], 0x80)
		copy(validPEBinary[0x80:], []byte{'P', 'E', 0, 0})
	} else {
		copy(validPEBinary[0:], []byte{0x7f, 'E', 'L', 'F'})
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(validPEBinary)
	}))
	defer srv.Close()

	hasher := protocol.ReleaseManifest{
		Version:                 "1.0.0",
		OSName:                  runtime.GOOS,
		Arch:                    runtime.GOARCH,
		SHA256Checksum:          "",
		Size:                    int64(len(validPEBinary)),
		URL:                     "/download",
		PublishedAt:             time.Now().UTC().Format(time.RFC3339),
		MinimumSupportedVersion: "0.1.0",
	}

	// Hash calculation for payload
	manifestBytes, _ := json.Marshal(validPEBinary)
	_ = manifestBytes

	// Real checksum calculation
	hSum := sha256.Sum256(validPEBinary)
	csum := fmt.Sprintf("%x", hSum[:])
	hasher.SHA256Checksum = csum

	sig, err := hasher.Sign(privKey)
	if err != nil {
		t.Fatalf("sign manifest: %v", err)
	}

	engine := update.NewEngine(srv.URL, "dev-1", "sec-1").WithSigningKey(pubKey, "0.5.0")
	// Override target path to temp dir
	targetBin := filepath.Join(t.TempDir(), "agent.exe")
	_ = os.WriteFile(targetBin, validPEBinary, 0755)
	engine.SetExecutablePath(targetBin)

	params := update.UpdateParams{
		TaskID:         "task-1",
		TargetVersion:  "1.0.0",
		DownloadURL:    "/download",
		SHA256Checksum: csum,
		FileSize:       int64(len(validPEBinary)),
		Manifest: &update.SignedManifest{
			Manifest:  hasher,
			Signature: sig,
		},
	}

	ctx := context.Background()
	err = engine.ApplyUpdate(ctx, params)
	if err != nil {
		t.Fatalf("ApplyUpdate failed: %v", err)
	}

	// Now test bad signature rejection
	badParams := params
	badParams.TaskID = "task-2"
	badParams.Manifest = &update.SignedManifest{
		Manifest:  hasher,
		Signature: base64.StdEncoding.EncodeToString(make([]byte, 64)),
	}

	errBad := engine.ApplyUpdate(ctx, badParams)
	if errBad == nil {
		t.Errorf("expected error for bad signature, got nil")
	}

	// Now test downgrade floor rejection
	downgradeEngine := update.NewEngine(srv.URL, "dev-1", "sec-1").WithSigningKey(pubKey, "0.0.1")
	downgradeEngine.SetExecutablePath(targetBin)
	highMinManifest := hasher
	highMinManifest.MinimumSupportedVersion = "2.0.0"
	highSig, _ := highMinManifest.Sign(privKey)

	downgradeParams := params
	downgradeParams.TaskID = "task-3"
	downgradeParams.Manifest = &update.SignedManifest{
		Manifest:  highMinManifest,
		Signature: highSig,
	}

	errDowngrade := downgradeEngine.ApplyUpdate(ctx, downgradeParams)
	if errDowngrade == nil {
		t.Errorf("expected error for downgrade below floor, got nil")
	}
}
