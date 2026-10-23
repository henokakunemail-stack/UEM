//go:build windows

package main

import (
	"os"
	"path/filepath"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo"
	winagent "github.com/henokakunemail-stack/Endpoint-Manager/agent/windows"
	"github.com/rs/zerolog/log"
)

// syncInstallRecord brings the Programs and Features entry up to the running
// version and icon. Best effort: a failure costs a stale entry, never the agent.
func syncInstallRecord() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if err := winagent.SyncInstallRecord(osinfo.Version, filepath.Dir(exe)); err != nil {
		log.Warn().Err(err).Msg("update the Programs and Features entry")
	}
}
