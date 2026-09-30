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
