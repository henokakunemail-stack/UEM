//go:build linux

package main

import (
	linagent "github.com/henokakunemail-stack/Endpoint-Manager/agent/linux"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo"
)

// newOSInfoProvider returns the Linux implementation.
func newOSInfoProvider() osinfo.Provider { return linagent.New() }
