//go:build darwin

package macos

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo"
)

// Provider implements osinfo.Provider for macOS via `sw_vers`.
type Provider struct{}

func New() Provider { return Provider{} }

func (Provider) Collect() (osinfo.Info, error) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	product, build := swVers()
	return osinfo.Info{
		Name: "macos", Version: fmt.Sprintf("%s (%s)", product, build),
		Hostname: hostname, AgentVersion: osinfo.Version,
	}, nil
}

// swVers returns (ProductVersion, BuildVersion) from `sw_vers`.
func swVers() (string, string) {
	out, err := exec.Command("sw_vers").CombinedOutput()
	if err != nil {
		return "unknown", "unknown"
	}
	product, build := "unknown", "unknown"
	for _, line := range strings.Split(string(out), "\n") {
		if v := strings.TrimSpace(strings.TrimPrefix(line, "ProductVersion:")); v != "" && v != line {
			product = v
		}
		if v := strings.TrimSpace(strings.TrimPrefix(line, "BuildVersion:")); v != "" && v != line {
			build = v
		}
	}
	return product, build
}
