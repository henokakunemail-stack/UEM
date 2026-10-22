package agentupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

type Auditor interface {
	Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error
}

// TaskStatusPending is the state a task is created in, and the one the
// reconnect path scans for. A task is only 'dispatched' once the command has
// actually been written to a device's socket, which is what
// Repository.MarkDispatched records.
const TaskStatusPending = "pending"

// TaskStatusFailed is terminal; the error message says why.
const TaskStatusFailed = "failed"

// HubDispatcher is the slice of the transport hub this module uses. Declared
// as an interface rather than taking *transport.Hub, matching the six other
// modules that talk to the hub: it keeps the dependency one-way and lets the
// reconnect flush be tested without standing up a real WebSocket.
type HubDispatcher interface {
	Online(deviceID string) bool
	SendTo(deviceID string, msg []byte) bool
}

type Handler struct {
	repo       *Repository
	hub        HubDispatcher
	deviceRepo *devicemgmt.Repository
	auditor    Auditor
	storageDir string
	authMw     func(http.Handler) http.Handler
	// publicKey is the release-signing key this server records signatures for.
	// Nil when UPDATE_SIGNING_PUBLIC_KEY is unset, in which case the sign
	// endpoint refuses rather than storing a signature it has no way to check.
	publicKey           ed25519.PublicKey
	minimumAgentVersion string
	// bundleDirs are where the server's release package keeps the agents it
	// ships with; serverVersion is the version those agents were built as.
	bundleDirs    []string
	serverVersion string
}

func NewHandler(
	repo *Repository,
	hub HubDispatcher,
	deviceRepo *devicemgmt.Repository,
	auditor Auditor,
	storageDir string,
	authMw func(http.Handler) http.Handler,
	minimumAgentVersion string,
) *Handler {
	_ = os.MkdirAll(storageDir, 0755)
	return &Handler{
		repo:                repo,
		hub:                 hub,
		deviceRepo:          deviceRepo,
		auditor:             auditor,
		storageDir:          storageDir,
		authMw:              authMw,
		minimumAgentVersion: minimumAgentVersion,
	}
}

// WithSigningPublicKey trusts the base64 Ed25519 public key whose signatures
// this server records, and sets the fleet-wide version floor it publishes.
//
// Both are deployment inputs. The key is the server's copy of the public half
// only; the private key never reaches this process, which is what makes a
// recorded signature worth more to an agent than the checksum that ships with
// it. An unset or unusable key yields a nil key and a warning rather than a
// startup failure, because a fleet that has not generated a key pair yet still
// has to be able to start and serve unsigned releases.
func (h *Handler) WithSigningPublicKey(b64 string, minimumAgentVersion string) *Handler {
	h.minimumAgentVersion = minimumAgentVersion
	if b64 == "" {
		log.Warn().Msg("UPDATE_SIGNING_PUBLIC_KEY is not set: releases cannot be signed, " +
			"and agents built with a trusted key will refuse every release this server uploads")
		return h
	}
	key, err := loadPublicKey(b64)
	if err != nil {
		log.Error().Err(err).Msg("UPDATE_SIGNING_PUBLIC_KEY is set but unusable; " +
			"signing stays disabled until it holds a valid base64 Ed25519 public key")
		return h
	}
	h.publicKey = key
	log.Info().Msg("release signature verification is enabled")
	return h
}

// loadPublicKey decodes the operator's base64 Ed25519 public key. A key that is
// the wrong length decodes to a zero key that would verify nothing, and base64
// padding noise is the most likely typo, so both are reported by name.
func loadPublicKey(b64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode UPDATE_SIGNING_PUBLIC_KEY: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("UPDATE_SIGNING_PUBLIC_KEY is %d bytes, want %d",
			len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

func (h *Handler) Register(r chi.Router) {
	// Agent endpoints (secret/token verified or device authenticated)
	r.Post("/api/agent/devices/{id}/update/report", h.handleAgentReport)
	r.Get("/api/agent/releases/{id}/download", h.handleAgentDownload)
	// /api/agent/config is unauthenticated by design: it publishes the release
	// signing key and the fleet version floor, which an agent needs before it
	// has any reason to trust a download, and it carries nothing sensitive.
	r.Get("/api/agent/config", h.handleAgentConfig)

	// Operator Console API
	r.Group(func(cr chi.Router) {
		cr.Use(h.authMw)

		// Releases
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/agent-updates/releases", h.handleListReleases)
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/agent-updates/releases/{id}", h.handleGetRelease)
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/agent-updates/releases/{id}/download", h.handleDownloadRelease)
		cr.With(rbac.RequireRole(rbac.RoleAdmin)).Post("/api/agent-updates/releases", h.handleUploadRelease)
		// Signing is its own permission from upload because the two are done by
		// different people in different places: the release is uploaded from the
		// console, and the signature comes from the signing host that holds the
		// private key. Neither grant implies the other.
		cr.With(rbac.RequireRole(rbac.RoleAdmin)).Post("/api/agent-updates/releases/{id}/sign", h.handleSignRelease)
		// The agents shipped inside the server's release package.
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/agent-updates/bundled", h.handleListBundled)
		cr.With(rbac.RequireRole(rbac.RoleAdmin)).Post("/api/agent-updates/bundled/import", h.handleImportBundled)

		// Campaigns
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/agent-updates/campaigns", h.handleListCampaigns)
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/agent-updates/campaigns/{id}", h.handleGetCampaign)
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/agent-updates/campaigns/{id}/tasks", h.handleGetCampaignTasks)
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/agent-updates/campaigns/{id}/logs", h.handleGetCampaignLogs)
		cr.With(rbac.RequireRole(rbac.RoleAdmin)).Post("/api/agent-updates/campaigns", h.handleCreateCampaign)
		cr.With(rbac.RequireRole(rbac.RoleAdmin)).Post("/api/agent-updates/campaigns/{id}/start", h.handleStartCampaign)
		cr.With(rbac.RequireRole(rbac.RoleAdmin)).Post("/api/agent-updates/campaigns/{id}/cancel", h.handleCancelCampaign)

		// Device Update Controls
		cr.With(rbac.RequireRole(rbac.RoleTechnician)).Post("/api/devices/{id}/update/dispatch", h.handleDispatchDeviceUpdate)
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/devices/{id}/update/status", h.handleGetDeviceUpdateStatus)
	})
}

func (h *Handler) handleUploadRelease(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 100<<20) // 100 MB max
	err := r.ParseMultipartForm(100 << 20)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to parse multipart form"})
		return
	}

	// Agents report their version without a "v", and a campaign compares the
	// two as strings, so a release typed as "v1.6.0" would never be reached.
	version := strings.TrimPrefix(strings.TrimSpace(r.FormValue("version")), "v")
	osName := r.FormValue("os_name")
	arch := r.FormValue("arch")
	changelog := r.FormValue("changelog")

	if version == "" || osName == "" || arch == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "version, os_name, and arch are required"})
		return
	}
	// Stored as the agent reports it, so a rollout can match it: "darwin" from
	// a build script or an older console becomes "macos", "x86_64" becomes
	// "amd64".
	osName = NormalizeOSName(osName)
	arch = normalizeArch(arch)

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file field is required"})
		return
	}
	defer file.Close()

	relID := newID()
	destPath, err := releasePath(h.storageDir, version, osName, arch, header.Filename)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	destFile, err := os.Create(destPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create destination file"})
		return
	}
	defer destFile.Close()

	hasher := sha256.New()
	writer := io.MultiWriter(destFile, hasher)

	written, err := io.Copy(writer, file)
	if cerr := destFile.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(destPath)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to write release file"})
		return
	}

	// A Go binary says what it was built for and, from a release build, which
	// version it is. A file whose own build disagrees with the form is refused:
	// a Linux build registered as Windows fails every device's format check
	// after the download, and a binary whose stamped version is not the
	// release version reports that other version when it reconnects, so the
	// device never reaches the campaign's target and the rollout cannot finish.
	if facts := inspectBinary(destPath); facts.IsGo {
		var mismatch string
		switch {
		case facts.OSName != osName || facts.Arch != arch:
			mismatch = fmt.Sprintf("the file is built for %s/%s, not %s/%s", facts.OSName, facts.Arch, osName, arch)
		case facts.Version != "" && facts.Version != version:
			mismatch = fmt.Sprintf("the file reports agent version %s, not %s", facts.Version, version)
		}
		if mismatch != "" {
			_ = os.Remove(destPath)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": mismatch, "code": "release_binary_mismatch"})
			return
		}
	}

	checksum := hex.EncodeToString(hasher.Sum(nil))
	actorID := auth.UserIDFromContext(r.Context())

	release := &AgentRelease{
		ID:                      relID,
		Version:                 version,
		OSName:                  osName,
		Arch:                    arch,
		FilePath:                destPath,
		FileSize:                written,
		SHA256Checksum:          checksum,
		Changelog:               changelog,
		IsActive:                true,
		UploadedBy:              actorID,
		MinimumSupportedVersion: r.FormValue("minimum_supported_version"),
	}

	if err := h.repo.CreateRelease(r.Context(), release); err != nil {
		_ = os.Remove(destPath)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	_ = h.auditor.Log(r.Context(), "user", actorID, "agent_update.release_upload", relID, map[string]string{
		"version":  version,
		"os_name":  osName,
		"arch":     arch,
		"checksum": checksum,
	})

	writeJSON(w, http.StatusCreated, release.toDTO())
}

// handleSignRelease records a signature a signing host produced off-box.
//
// The server never holds the private key, so it cannot make this signature --
// it can only check one. That ordering is what the endpoint enforces: the
// caller's manifest is rebuilt server-side from the release row and the
// signature is verified against the deployed public key before anything is
// written. A signature that does not verify is rejected, and so is a signature
// that verifies but covers a manifest that does not match the row, because
// either one would let a caller attach a real signature from a different
// artifact to a release of its own choosing.
//
// The caller still names its own manifest fields, so a signing job that has the
// wrong artifact path or the wrong floor fails loudly here rather than silently
// certifying the wrong thing. Only the signature itself is not an input: it is
// checked, never trusted.
func (h *Handler) handleSignRelease(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.publicKey == nil {
		// Fail closed with the reason, because this is the one endpoint whose
		// whole purpose is to attest, and 500 would read as "the server is
		// broken" when the real state is "this deployment cannot sign yet".
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "release signing is not configured on this server (UPDATE_SIGNING_PUBLIC_KEY)",
		})
		return
	}

	var req struct {
		Signature               string `json:"signature"`
		MinimumSupportedVersion string `json:"minimum_supported_version"`
		DownloadURL             string `json:"download_url"`
		PublishedAt             string `json:"published_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.Signature == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "signature is required"})
		return
	}

	rel, err := h.repo.GetRelease(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "release not found"})
		return
	}

	publishedAt := time.Now().UTC()
	if req.PublishedAt != "" {
		publishedAt, err = time.Parse(time.RFC3339, req.PublishedAt)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "published_at must be RFC3339, e.g. 2026-10-07T12:00:00Z",
			})
			return
		}
	}

	// Rebuild the manifest the way the agent will, from the row the caller is
	// asking to sign. The signature is verified against exactly this, so a
	// caller whose local manifest disagrees with the row -- a stale artifact, a
	// different release id, a checksum from a file it has not re-hashed -- gets
	// a rejection naming the field, not a recorded signature over something
	// else.
	want := manifestForRelease(rel, req.DownloadURL, publishedAt)
	want.MinimumSupportedVersion = req.MinimumSupportedVersion
	if err := want.Verify(h.publicKey, req.Signature); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "signature does not verify against this release's manifest: " + err.Error(),
		})
		return
	}

	if err := h.repo.SignRelease(r.Context(), id, req.Signature,
		req.MinimumSupportedVersion, req.DownloadURL, publishedAt); err != nil {
		if errors.Is(err, ErrReleaseNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "release not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.auditor.Log(r.Context(), "user", actorID, "agent_update.release_signed", id, map[string]string{
		"version":                   rel.Version,
		"minimum_supported_version": req.MinimumSupportedVersion,
		"download_url":              req.DownloadURL,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"release_id":                id,
		"signed":                    true,
		"minimum_supported_version": req.MinimumSupportedVersion,
		"download_url":              req.DownloadURL,
		"published_at":              publishedAt.UTC().Format(time.RFC3339),
	})
}

// handleAgentConfig publishes the release-trust configuration an agent needs
// before it has any reason to trust a download. Unauthenticated, because it
// contains only public material: the signing key the fleet's releases are
// stamped with, and the oldest version this deployment still supports.
//
// The key here is advisory for an operator or a provisioning tool. An agent
// that was built with a key embedded does not fetch or honour this value --
// trusting the server for the key that is supposed to constrain the server
// would defeat the point. Agents that were not built with one read the checksum
// alone, which is what they did before this endpoint existed.
func (h *Handler) handleAgentConfig(w http.ResponseWriter, r *http.Request) {
	publicKey := ""
	if h.publicKey != nil {
		publicKey = base64.StdEncoding.EncodeToString(h.publicKey)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"public_key":              publicKey,
		"minimum_agent_version":   h.minimumAgentVersion,
		"signature_required_hint": publicKey != "",
	})
}

func (h *Handler) handleListReleases(w http.ResponseWriter, r *http.Request) {
	list, err := h.repo.ListReleases(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Never hand the browser the server-side absolute path.
	out := make([]AgentReleaseDTO, 0, len(list))
	for _, rel := range list {
		out = append(out, rel.toDTO())
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) handleGetRelease(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rel, err := h.repo.GetRelease(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "release not found"})
		return
	}
	writeJSON(w, http.StatusOK, rel.toDTO())
}

func (h *Handler) handleDownloadRelease(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rel, err := h.repo.GetRelease(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "release not found"})
		return
	}
	http.ServeFile(w, r, rel.FilePath)
}

func (h *Handler) handleAgentDownload(w http.ResponseWriter, r *http.Request) {
	if _, ok := devicemgmt.AuthenticateAgent(w, r, h.deviceRepo); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	rel, err := h.repo.GetRelease(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "release not found"})
		return
	}
	http.ServeFile(w, r, rel.FilePath)
}

func (h *Handler) handleCreateCampaign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name               string `json:"name"`
		Description        string `json:"description"`
		TargetVersion      string `json:"target_version"`
		TargetType         string `json:"target_type"`
		TargetID           string `json:"target_id"`
		BatchSize          int    `json:"batch_size"`
		StaggerIntervalSec int    `json:"stagger_interval_sec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	req.TargetVersion = strings.TrimPrefix(strings.TrimSpace(req.TargetVersion), "v")
	if req.Name == "" || req.TargetVersion == "" || req.TargetType == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name, target_version, and target_type are required"})
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	campaign := &UpdateCampaign{
		Name:               req.Name,
		Description:        req.Description,
		TargetVersion:      req.TargetVersion,
		TargetType:         req.TargetType,
		TargetID:           req.TargetID,
		BatchSize:          req.BatchSize,
		StaggerIntervalSec: req.StaggerIntervalSec,
		CreatedBy:          actorID,
	}

	if err := h.repo.CreateCampaign(r.Context(), campaign); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	_ = h.auditor.Log(r.Context(), "user", actorID, "agent_update.campaign_create", campaign.ID, map[string]string{
		"name":           campaign.Name,
		"target_version": campaign.TargetVersion,
	})

	writeJSON(w, http.StatusCreated, campaign)
}

func (h *Handler) handleListCampaigns(w http.ResponseWriter, r *http.Request) {
	list, err := h.repo.ListCampaigns(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) handleGetCampaign(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	c, err := h.repo.GetCampaign(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "campaign not found"})
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *Handler) handleGetCampaignTasks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tasks, err := h.repo.ListCampaignTasks(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (h *Handler) handleGetCampaignLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	logs, err := h.repo.ListCampaignLogs(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

func (h *Handler) handleCancelCampaign(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	c, err := h.repo.GetCampaign(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "campaign not found"})
		return
	}
	actorID := auth.UserIDFromContext(r.Context())
	cancelled, err := h.repo.CancelCampaign(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !cancelled {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  fmt.Sprintf("cannot cancel campaign with status %s", c.Status),
			"code":   "campaign_not_cancellable",
			"status": c.Status,
		})
		return
	}

	_ = h.auditor.Log(r.Context(), "user", actorID, "agent_update.campaign_cancel", id, map[string]string{
		"campaign_id": id,
		"action":      "cancelled",
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (h *Handler) handleStartCampaign(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	campaign, err := h.repo.GetCampaign(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "campaign not found"})
		return
	}

	deviceIDs, err := h.repo.ResolveTargetDevices(r.Context(), campaign.TargetType, campaign.TargetID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("resolve devices: %v", err)})
		return
	}

	actorID := auth.UserIDFromContext(r.Context())

	// Pre-flight check: count devices needing update vs already up to date
	devicesNeedingUpdate := 0
	for _, devID := range deviceIDs {
		dev, err := h.deviceRepo.GetByID(r.Context(), devID)
		if err != nil {
			continue
		}
		if dev.AgentVersionString() != campaign.TargetVersion {
			devicesNeedingUpdate++
		}
	}

	// The claim happens at the branch, after the read-only pre-flight, so a
	// failed target lookup cannot leave a campaign stuck in in_progress. Only a
	// campaign that has not started can be started.
	notStartable := func() {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  fmt.Sprintf("campaign cannot be started from status %s", campaign.Status),
			"code":   "campaign_not_startable",
			"status": campaign.Status,
		})
	}

	if len(deviceIDs) > 0 && devicesNeedingUpdate == 0 {
		claimed, err := h.repo.TransitionCampaignStatus(r.Context(), id, "completed", "draft", "pending")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if !claimed {
			notStartable()
			return
		}
		_ = h.auditor.Log(r.Context(), "user", actorID, "agent_update.campaign_start", id, map[string]string{
			"campaign_id": id,
			"status":      "completed",
			"reason":      fmt.Sprintf("all %d target devices are already running %s", len(deviceIDs), campaign.TargetVersion),
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"status":          "completed",
			"total_targets":   len(deviceIDs),
			"dispatched_live": 0,
			"message":         fmt.Sprintf("All %d target devices are already running agent v%s", len(deviceIDs), campaign.TargetVersion),
		})
		return
	}

	claimed, err := h.repo.TransitionCampaignStatus(r.Context(), id, "in_progress", "draft", "pending")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !claimed {
		notStartable()
		return
	}

	// Every task is created now, in one transaction, so the campaign always
	// knows how much work is left. A device with no release for its OS and
	// architecture gets a failed task that says so instead of being dropped
	// from the rollout without a trace.
	tasks := make([]*DeviceUpdateTask, 0, len(deviceIDs))
	noRelease := 0
	now := time.Now().UTC()
	for _, devID := range deviceIDs {
		dev, err := h.deviceRepo.GetByID(r.Context(), devID)
		if err != nil || dev.AgentVersionString() == campaign.TargetVersion {
			continue
		}
		task := &DeviceUpdateTask{
			CampaignID:    &campaign.ID,
			DeviceID:      devID,
			FromVersion:   dev.AgentVersionString(),
			TargetVersion: campaign.TargetVersion,
			Status:        TaskStatusPending,
		}
		arch := h.repo.GetDeviceArch(r.Context(), devID)
		if _, err := h.repo.GetActiveReleaseForDevice(r.Context(), campaign.TargetVersion, dev.OSName, arch); err != nil {
			task.Status = TaskStatusFailed
			task.ErrorMessage = fmt.Sprintf("no_release: no active release of %s for %s/%s", campaign.TargetVersion, dev.OSName, arch)
			task.CompletedAt = &now
			task.ReleasedAt = &now
			noRelease++
		}
		tasks = append(tasks, task)
	}
	if err := h.repo.CreateCampaignTasks(r.Context(), tasks); err != nil {
		// Hand the campaign back so it can be started again; nothing was sent.
		_, _ = h.repo.TransitionCampaignStatus(r.Context(), id, "draft", "in_progress")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	dispatched, queued, err := h.releaseWave(r.Context(), campaign)
	if err != nil {
		log.Warn().Err(err).Str("campaign_id", id).Msg("release first wave")
	}
	if err := h.repo.SyncCampaignStatusByID(r.Context(), campaign.ID); err != nil {
		log.Warn().Err(err).Str("campaign_id", id).Msg("roll up campaign")
	}

	_ = h.auditor.Log(r.Context(), "user", actorID, "agent_update.campaign_start", id, map[string]string{
		"dispatched": fmt.Sprintf("%d", dispatched),
		"queued":     fmt.Sprintf("%d", queued),
		"no_release": fmt.Sprintf("%d", noRelease),
		"target":     campaign.TargetType,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"status":          "started",
		"total_targets":   len(deviceIDs),
		"tasks":           len(tasks),
		"dispatched_live": dispatched,
		"queued":          queued,
		"no_release":      noRelease,
	})
}

// releaseWave lets the campaign's next batch go: online devices are sent the
// update now, offline ones keep a released task that the reconnect flush
// sends. It returns how many went out and how many were queued.
func (h *Handler) releaseWave(ctx context.Context, campaign *UpdateCampaign) (dispatched, queued int, err error) {
	stagger := time.Duration(campaign.StaggerIntervalSec) * time.Second
	if stagger < 0 {
		stagger = 0
	}
	released, err := h.repo.ReleaseNextWave(ctx, campaign.ID, campaign.BatchSize, stagger)
	for _, task := range released {
		if h.dispatchReleased(ctx, campaign, task) {
			dispatched++
		} else {
			queued++
		}
	}
	return dispatched, queued, err
}

// dispatchReleased sends one released task if its device is online and
// reports whether it did. A task that is not sent stays pending and released:
// queued for the reconnect flush, or for the wave loop's retry.
func (h *Handler) dispatchReleased(ctx context.Context, campaign *UpdateCampaign, task *DeviceUpdateTask) bool {
	details := map[string]string{
		"campaign_id":    campaign.ID,
		"task_id":        task.ID,
		"target_version": task.TargetVersion,
		"from_version":   task.FromVersion,
	}
	if !h.hub.Online(task.DeviceID) {
		details["status"] = "pending_offline"
		_ = h.auditor.Log(ctx, "system", task.DeviceID, "agent_update.task_queued", task.DeviceID, details)
		return false
	}
	dev, err := h.deviceRepo.GetByID(ctx, task.DeviceID)
	if err != nil {
		return false
	}
	arch := h.repo.GetDeviceArch(ctx, task.DeviceID)
	rel, err := h.repo.GetActiveReleaseForDevice(ctx, task.TargetVersion, dev.OSName, arch)
	if err != nil {
		_ = h.repo.RecordTaskProgress(ctx, task.ID, TaskStatusFailed,
			fmt.Sprintf("no_release: no active release of %s for %s/%s", task.TargetVersion, dev.OSName, arch))
		return false
	}
	if err := h.SendTask(ctx, task, rel); err != nil {
		log.Warn().Err(err).Str("device", task.DeviceID).Str("task_id", task.ID).Msg("send update; will retry")
		return false
	}
	details["status"] = "dispatched"
	_ = h.auditor.Log(ctx, "system", task.DeviceID, "agent_update.task_dispatched", task.DeviceID, details)
	return true
}

// RunWaves releases due campaign waves and retries queued tasks for online
// devices until ctx is cancelled. It runs on the leader only.
//
// Waves used to be a goroutine per campaign sleeping on context.Background():
// a restart or a leader change lost every wave not yet sent, and the campaign
// sat in_progress with devices that were never told to update. The schedule
// now lives in the database (next_wave_at), so whichever node leads picks it
// up where the last one stopped.
func (h *Handler) RunWaves(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickCtx, cancel := context.WithTimeout(ctx, interval*3)
			h.WaveTick(tickCtx)
			cancel()
		}
	}
}

// WaveTick runs one pass of the wave loop.
func (h *Handler) WaveTick(ctx context.Context) {
	due, err := h.repo.DueWaveCampaigns(ctx, time.Now().UTC())
	if err != nil {
		log.Warn().Err(err).Msg("list due campaign waves")
	}
	for _, c := range due {
		if _, _, err := h.releaseWave(ctx, c); err != nil {
			log.Warn().Err(err).Str("campaign_id", c.ID).Msg("release campaign wave")
		}
		if err := h.repo.SyncCampaignStatusByID(ctx, c.ID); err != nil {
			log.Warn().Err(err).Str("campaign_id", c.ID).Msg("roll up campaign")
		}
	}

	devices, err := h.repo.DevicesWithQueuedTasks(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("list devices with queued updates")
		return
	}
	for _, devID := range devices {
		if !h.hub.Online(devID) {
			continue
		}
		if err := h.FlushPendingUpdates(ctx, devID); err != nil {
			log.Warn().Err(err).Str("device", devID).Msg("retry queued updates")
		}
	}
}

func (h *Handler) handleDispatchDeviceUpdate(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	dev, err := h.deviceRepo.GetByID(r.Context(), deviceID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not found"})
		return
	}

	var req struct {
		TargetVersion string `json:"target_version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.TargetVersion = strings.TrimPrefix(strings.TrimSpace(req.TargetVersion), "v")
	if req.TargetVersion == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target_version is required"})
		return
	}

	arch := h.repo.GetDeviceArch(r.Context(), deviceID)
	rel, err := h.repo.GetActiveReleaseForDevice(r.Context(), req.TargetVersion, dev.OSName, arch)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("no active release found for %s/%s/%s", req.TargetVersion, dev.OSName, arch),
		})
		return
	}

	now := time.Now().UTC()
	fromVersion := dev.AgentVersionString()
	task := &DeviceUpdateTask{
		DeviceID:      deviceID,
		FromVersion:   fromVersion,
		TargetVersion: req.TargetVersion,
		// Not 'dispatched'. The device is checked for liveness below, and
		// stamping the row as sent before that check is what made "queued" a
		// status word in the response and nothing more: the task sat in
		// 'dispatched' with nothing to move it, because update.apply is only
		// built here and in the campaign path, both behind hub.Online, and the
		// offline sweeper only sweeps deployment_tasks. It is created pending
		// and SendTask stamps it, so an offline device leaves a row that the
		// reconnect path can actually find.
		Status:       TaskStatusPending,
		DispatchedAt: &now,
	}
	if err := h.repo.CreateUpdateTask(r.Context(), task); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if !h.hub.Online(deviceID) {
		// Corrected to match the row: this is queued, and the row says pending.
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "queued",
			"task_id": task.ID,
			"message": "device is currently offline; the update will be sent when it reconnects",
		})
		return
	}

	if err := h.SendTask(r.Context(), task, rel); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "device connection busy"})
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.auditor.Log(r.Context(), "user", actorID, "agent_update.device_dispatched", deviceID, map[string]string{
		"task_id":        task.ID,
		"target_version": req.TargetVersion,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "dispatched",
		"task_id":        task.ID,
		"target_version": req.TargetVersion,
	})
}

func (h *Handler) handleGetDeviceUpdateStatus(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	task, err := h.repo.GetLatestTaskForDevice(r.Context(), deviceID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no update task found for device"})
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// SendTask delivers one update command to a device and marks the task sent.
//
// The payload used to be built inline at each of the two call sites, which is
// what let them drift: this one existed to also be reachable from the reconnect
// path. Both orderings matter. The stamp goes first so a second caller -- a
// reconnect racing the operator's Dispatch, or two reconnects at once -- cannot
// send the same command twice, since the mark is conditional on the task still
// being pending. The write goes second, so a task whose send failed stays
// pending and is retried on the next reconnect rather than being silently lost.
func (h *Handler) SendTask(ctx context.Context, task *DeviceUpdateTask, rel *AgentRelease) error {
	if err := h.repo.MarkDispatched(ctx, task.ID); err != nil {
		if errors.Is(err, ErrAlreadyDispatched) {
			return nil // someone else sent it; not a failure
		}
		return err
	}
	payload := map[string]any{
		"task_id":         task.ID,
		"target_version":  task.TargetVersion,
		"download_url":    releaseDownloadURL(rel),
		"sha256_checksum": rel.SHA256Checksum,
		"file_size":       rel.FileSize,
	}
	// The signed manifest rides along with the artifact reference. An agent that
	// was built with a trusted key verifies it before it swaps its own
	// executable; one that was not ignores it and falls back to the checksum
	// alone. See engine.verifyManifest for why a missing manifest on a
	// key-carrying agent is a refusal rather than a fallback.
	if rel.Ed25519Signature != "" {
		payload["manifest"] = protocol.SignedManifest{
			Manifest:  manifestForRelease(rel, releaseDownloadURL(rel), publishedAtOf(rel)),
			Signature: rel.Ed25519Signature,
		}
	}
	msg, err := json.Marshal(map[string]any{
		"type":    "command",
		"id":      task.ID,
		"command": "update.apply",
		"payload": payload,
	})
	if err != nil {
		return err
	}
	if !h.hub.SendTo(task.DeviceID, msg) {
		// Hand the task back so the next reconnect picks it up. Without this the
		// row says dispatched, the agent never got the command, and nothing would
		// ever look at it again.
		_ = h.repo.RequeueTask(ctx, task.ID)
		return errors.New("device connection busy")
	}
	return nil
}

// FlushPendingUpdates sends the update tasks left waiting for a device that has
// just come back online. It is the transport.UpdateQueue hook, called from the
// agent websocket handler right after the device is marked online.
//
// This is what makes the word "queued" in handleDispatchDeviceUpdate's response
// true. It was not: the task row said 'dispatched', nothing re-read it, and the
// offline sweeper only covers deployment_tasks, so an update an operator
// queued against a sleeping laptop was dropped silently and left the fleet's
// version inventory claiming the device had been upgraded.
//
// A task whose release has since been deactivated is skipped rather than sent,
// and the reason is recorded on the row: a queued update is a promise, and
// quietly dropping it leaves the operator with a task that never resolves and
// no explanation for it.
func (h *Handler) FlushPendingUpdates(ctx context.Context, deviceID string) error {
	tasks, err := h.repo.PendingTasksFor(ctx, deviceID)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return nil
	}

	dev, err := h.deviceRepo.GetByID(ctx, deviceID)
	if err != nil {
		// The device row is gone (retired or deleted). Leave the tasks pending
		// rather than failing them: a task with no device is an operator's to
		// resolve, and guessing "failed" here would silently drop the intent.
		log.Warn().Err(err).Str("device", deviceID).
			Msg("cannot flush pending updates: device row unreadable")
		return nil
	}

	arch := h.repo.GetDeviceArch(ctx, deviceID)
	for _, task := range tasks {
		rel, err := h.repo.GetActiveReleaseForDevice(ctx, task.TargetVersion, dev.OSName, arch)
		if err != nil {
			_ = h.repo.RecordTaskProgress(ctx, task.ID, "failed",
				"no active release for "+task.TargetVersion+"/"+dev.OSName+"/"+arch+
					" when the device reconnected; re-upload the release or re-queue a different version")
			log.Warn().Err(err).Str("device", deviceID).Str("task_id", task.ID).
				Msg("pending update has no active release")
			continue
		}
		if err := h.SendTask(ctx, task, rel); err != nil {
			log.Warn().Err(err).Str("device", deviceID).Str("task_id", task.ID).
				Msg("send pending update")
			continue
		}
		_ = h.auditor.Log(ctx, "system", deviceID, "agent_update.queued_flushed", deviceID,
			map[string]string{"task_id": task.ID, "version": task.TargetVersion})
	}
	return nil
}

func (h *Handler) handleAgentReport(w http.ResponseWriter, r *http.Request) {
	// Authentication alone is not enough, and a comment here used to claim it
	// was. The device id in the URL is attacker-controlled: any client that
	// could reach the port could name a device it does not own, and the code
	// below would have marked that device's rollout successful and overwritten
	// its agent_version. AuthenticateAgent proves who is asking; it says nothing
	// about whose rollout this is, so the two are compared here.
	authedID, ok := devicemgmt.AuthenticateAgent(w, r, h.deviceRepo)
	if !ok {
		return
	}
	deviceID := chi.URLParam(r, "id")
	if deviceID != authedID {
		// 404 rather than 403: the caller is a real enrolled agent, so telling
		// it that the device exists but is not its own is more than it needs to
		// know about the fleet.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not found"})
		return
	}

	var req struct {
		TaskID        string `json:"task_id"`
		Status        string `json:"status"` // 'downloading', 'verifying', 'swapping', 'success', 'rollback', 'failed'
		TargetVersion string `json:"target_version"`
		ErrorMessage  string `json:"error_message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	// Scoped as well as the device, because the task id arrives in the body and
	// is just as attacker-controlled as the one in the path. Otherwise an agent
	// could advance any task in the fleet while its own device id checked out.
	if req.TaskID != "" {
		owner, err := h.repo.TaskDeviceID(r.Context(), req.TaskID)
		if err != nil || owner != deviceID {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
			return
		}
	}

	// A rejected regress is the guard working, not a fault: ErrTaskAlreadyFinished
	// means this exact report already landed and the task had already finished
	// when it did. Anything else is a real write failure and stays a warning.
	if err := h.repo.RecordTaskProgress(r.Context(), req.TaskID, req.Status, req.ErrorMessage); err != nil {
		if errors.Is(err, ErrTaskAlreadyFinished) {
			log.Debug().Err(err).Str("task_id", req.TaskID).Str("status", req.Status).
				Msg("ignored a progress report for a finished update task")
		} else {
			log.Warn().Err(err).Str("task_id", req.TaskID).Msg("failed to update task progress")
		}
	}

	// Log status transparently to audit trail so operators can track rollout stages
	switch req.Status {
	case "failed", "rollback":
		_ = h.auditor.Log(r.Context(), "device", deviceID, "agent_update.task_failed", deviceID, map[string]string{
			"task_id":        req.TaskID,
			"status":         req.Status,
			"target_version": req.TargetVersion,
			"error":          req.ErrorMessage,
		})
	case "downloading", "verifying", "swapping":
		_ = h.auditor.Log(r.Context(), "device", deviceID, "agent_update.task_progress", deviceID, map[string]string{
			"task_id":        req.TaskID,
			"status":         req.Status,
			"target_version": req.TargetVersion,
		})
	}

	if req.Status == "success" && req.TargetVersion != "" {
		if err := h.repo.UpdateDeviceAgentVersion(r.Context(), deviceID, req.TargetVersion); err != nil {
			log.Error().Err(err).Str("device", deviceID).Str("version", req.TargetVersion).
				Msg("update reported success but the device version could not be recorded")
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "update recorded but the device version could not be saved; report again",
			})
			return
		}
		_ = h.auditor.Log(r.Context(), "device", deviceID, "agent_update.task_success", deviceID, map[string]string{
			"task_id": req.TaskID,
			"version": req.TargetVersion,
			"status":  "success",
		})
	}

	// The campaign rollup runs after the version write, not after the task
	// write, so a 'success' that could not be recorded never counts toward a
	// completed rollout.
	if req.TaskID != "" {
		if err := h.repo.SyncCampaignStatus(r.Context(), req.TaskID); err != nil {
			log.Warn().Err(err).Str("task_id", req.TaskID).Msg("roll up campaign status")
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
}

// ReconcileDeviceVersion reconciles in-flight update tasks if an agent reconnects
// and reports the target version during its hello handshake.
func (h *Handler) ReconcileDeviceVersion(ctx context.Context, deviceID, version string) error {
	if deviceID == "" || version == "" {
		return nil
	}
	tasks, err := h.repo.ActiveTasksForDevice(ctx, deviceID)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if task.TargetVersion == version {
			_ = h.repo.RecordTaskProgress(ctx, task.ID, "success", "")
			_ = h.repo.UpdateDeviceAgentVersion(ctx, deviceID, version)
			_ = h.auditor.Log(ctx, "device", deviceID, "agent_update.hello_reconciled", deviceID, map[string]string{
				"task_id": task.ID,
				"version": version,
				"status":  "success",
				"reason":  "Agent reconnected with target version via WebSocket",
			})
			_ = h.repo.SyncCampaignStatus(ctx, task.ID)
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// releasePath builds the on-disk name for an uploaded release, and refuses any
// input that would place the file outside storageDir.
//
// version, os_name and arch are operator-supplied form fields, and they used to
// be interpolated straight into filepath.Join. filepath.Join calls Clean, which
// resolves "..", so a version of "..\..\startup" produced
//
//	Join("data/agent-releases", "..\..\startup_win_x64_update.exe")
//	 = "data/startup_win_x64_update.exe"
//
// one directory up from storage, with the upload returning 201 as though it had
// succeeded. On Windows a version like "..\..\Windows\System32" reaches a
// system directory. This is a write outside the directory the operator granted,
// and the write primitive it composes with is what gets shipped to every device
// in the fleet.
//
// The real defence is not the sanitising: an upload endpoint is a write
// primitive by design, and an admin who can upload a file can already upload
// one. It is that the file lands where the code says it lands. The filename is
// still built from the submitted version, because that is what the console
// shows and what the download route later has to match, so instead of quietly
// rewriting the name -- which would leave the database pointing at a file that
// does not exist -- a hostile version is rejected while the operator is still
// on the upload form and can retype it.
//
// headerFilename is reduced to its base name for the same reason: the multipart
// filename is attacker-controlled too, and an absolute or traversing one would
// otherwise reach the join from the other side.
func releasePath(storageDir, version, osName, arch, headerFilename string) (string, error) {
	cleanDir := filepath.Clean(storageDir)
	for _, f := range []struct{ name, value string }{
		{"version", version},
		{"os_name", osName},
		{"arch", arch},
	} {
		// Reject the separators and the relative segments rather than the whole
		// field: a version string legitimately contains dots ("1.2.3") and a
		// platform name may contain a dash. What it may never contain is a path.
		if f.value == "" {
			return "", fmt.Errorf("%s is required", f.name)
		}
		if strings.ContainsAny(f.value, `/\`) || f.value == ".." || f.value == "." {
			return "", fmt.Errorf("%s must not contain a path", f.name)
		}
	}

	name := fmt.Sprintf("%s_%s_%s_%s", version, osName, arch, filepath.Base(headerFilename))
	// Belt and braces: the result is checked against the directory rather than
	// assumed to be inside it, so a rule added above cannot silently regress
	// into the same escape this exists to close.
	full := filepath.Join(cleanDir, name)
	if filepath.Dir(full) != cleanDir {
		return "", fmt.Errorf("resolved filename escapes the release directory")
	}
	return full, nil
}
