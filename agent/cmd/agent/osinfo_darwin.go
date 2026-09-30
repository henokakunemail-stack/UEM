//go:build darwin

package main

import (
	macagent "github.com/henokakunemail-stack/Endpoint-Manager/agent/macos"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo"
)

// newOSInfoProvider returns the macOS implementation.
func newOSInfoProvider() osinfo.Provider { return macagent.New() }
