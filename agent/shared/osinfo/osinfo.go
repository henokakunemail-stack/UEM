// Package osinfo provides per-OS system facts. The interface lives in shared;
// each OS provides its implementation behind a build tag.
package osinfo

// Info describes the machine the agent runs on.
type Info struct {
	Name         string `json:"name"`    // windows|linux|macos
	Version      string `json:"version"` // OS version
	Hostname     string `json:"hostname"`
	AgentVersion string `json:"agent_version"`
}

// Provider collects OS facts. Implemented per-OS.
type Provider interface {
	Collect() (Info, error)
}

// Version is the agent build version, reported in every osinfo payload and used
// by the server to decide which devices are eligible for an update.
//
// It is a var, not a const, and it lives here rather than in a per-platform
// file, for one reason: release and E2E builds stamp it with
//
//	go build -ldflags "-X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version=1.2.3"
//
// That only works on a var in a package the binary actually links. It was
// previously a const in each of agent/windows, agent/linux and agent/macos
// while seven E2E scripts passed "-X main.agentVersion=...", so every one of
// them built a binary that still reported 0.1.0 and the versioning path was
// never actually exercised. Keep the default in sync with the release tag.
var Version = "0.1.0"
