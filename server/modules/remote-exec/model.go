package remoteexec

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// NewID produces a random 32-character hex ID.
func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

const (
	ShellPowerShell = "powershell"
	ShellCMD        = "cmd"
	ShellBash       = "bash"
	ShellSH         = "sh"

	ExecStatusPending   = "pending"
	ExecStatusRunning   = "running"
	ExecStatusCompleted = "completed"
	ExecStatusFailed    = "failed"
	ExecStatusTimeout   = "timeout"
	ExecStatusCancelled = "cancelled"

	TermStatusActive = "active"
	TermStatusClosed = "closed"
)

// RemoteExecution represents a non-interactive command run on a device.
type RemoteExecution struct {
	ID           string     `db:"id" json:"id"`
	DeviceID     string     `db:"device_id" json:"device_id"`
	OperatorID   string     `db:"operator_id" json:"operator_id"`
	ShellType    string     `db:"shell_type" json:"shell_type"`
	CommandText  string     `db:"command_text" json:"command_text"`
	Status       string     `db:"status" json:"status"`
	ExitCode     *int       `db:"exit_code" json:"exit_code,omitempty"`
	Output       *string    `db:"output" json:"output,omitempty"`
	ErrorMessage *string    `db:"error_message" json:"error_message,omitempty"`
	StartedAt    time.Time  `db:"started_at" json:"started_at"`
	CompletedAt  *time.Time `db:"completed_at" json:"completed_at,omitempty"`

	// Enriched fields for presentation
	OperatorName string `db:"operator_name" json:"operator_name,omitempty"`
	Hostname     string `db:"hostname" json:"hostname,omitempty"`
}

// TerminalSession represents an interactive live shell session.
type TerminalSession struct {
	ID         string     `db:"id" json:"id"`
	DeviceID   string     `db:"device_id" json:"device_id"`
	OperatorID string     `db:"operator_id" json:"operator_id"`
	ShellType  string     `db:"shell_type" json:"shell_type"`
	Status     string     `db:"status" json:"status"`
	CreatedAt  time.Time  `db:"created_at" json:"created_at"`
	ClosedAt   *time.Time `db:"closed_at" json:"closed_at,omitempty"`

	OperatorName string `db:"operator_name" json:"operator_name,omitempty"`
	Hostname     string `db:"hostname" json:"hostname,omitempty"`
}

// ExecRequest is the payload sent by operator to run a remote command.
type ExecRequest struct {
	Shell      string `json:"shell"` // 'powershell', 'cmd', 'bash', 'sh'
	Command    string `json:"command"`
	TimeoutSec int    `json:"timeout_sec,omitempty"` // default 60, max 300
}

// ExecResultReport is reported by the agent upon command completion.
type ExecResultReport struct {
	ExecutionID  string  `json:"execution_id"`
	Status       string  `json:"status"` // 'completed', 'failed', 'timeout'
	ExitCode     *int    `json:"exit_code,omitempty"`
	Output       *string `json:"output,omitempty"`
	ErrorMessage *string `json:"error_message,omitempty"`
}
