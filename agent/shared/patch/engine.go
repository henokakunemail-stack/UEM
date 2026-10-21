package patch

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/reportretry"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/transport"
)

type Engine struct {
	serverBase string
	deviceID   string
	secret     string
	client     *http.Client
}

func NewEngine(serverBase, deviceID, secret string) *Engine {
	base := strings.TrimRight(serverBase, "/")
	return &Engine{
		serverBase: base,
		deviceID:   deviceID,
		secret:     secret,
		client:     transport.NewHTTPClient(30 * time.Second),
	}
}

func (e *Engine) Scan(ctx context.Context) ([]PatchItem, error) {
	return scanOS(ctx)
}

func (e *Engine) Install(ctx context.Context, params InstallParams) (InstallResult, error) {
	return installOS(ctx, params)
}

// ReportScan sends the scanned patch list. rebootPending is nil when the OS
// gives no answer; the server then leaves its reboot flag untouched.
func (e *Engine) ReportScan(ctx context.Context, patches []PatchItem, rebootPending *bool) error {
	payload := map[string]any{"patches": patches}
	if rebootPending != nil {
		payload["reboot_pending"] = *rebootPending
	}
	return reportretry.Post(ctx, e.client, e.serverBase+"/api/agent/patches/scan-report", e.deviceID, e.secret, payload)
}

// ReportInstall sends the outcome of an install job. The job stays
// 'dispatched' on the server until this arrives, so it is retried.
func (e *Engine) ReportInstall(ctx context.Context, res InstallResult) error {
	return reportretry.Post(ctx, e.client, e.serverBase+"/api/agent/patches/install-result", e.deviceID, e.secret, res)
}
