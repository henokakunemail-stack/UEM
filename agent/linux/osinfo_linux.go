//go:build linux

package linux

import (
	"bufio"
	"os"
	"strings"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo"
)

// Provider implements osinfo.Provider for Linux via /etc/os-release.
type Provider struct{}

func New() Provider { return Provider{} }

func (Provider) Collect() (osinfo.Info, error) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	version := parseOSRelease()
	return osinfo.Info{
		Name: "linux", Version: version, Hostname: hostname, AgentVersion: osinfo.Version,
	}, nil
}

// parseOSRelease reads PRETTY_NAME from /etc/os-release, falling back to
// /etc/lsb-release for older distributions.
func parseOSRelease() string {
	if v := readKey("/etc/os-release", "PRETTY_NAME"); v != "" {
		return v
	}
	if v := readKey("/etc/lsb-release", "DISTRIB_DESCRIPTION"); v != "" {
		return v
	}
	return "unknown"
}

func readKey(path, key string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, key+"=") {
			continue
		}
		v := strings.TrimPrefix(line, key+"=")
		v = strings.Trim(v, `"`)
		return strings.TrimSpace(v)
	}
	return ""
}
