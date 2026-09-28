package transport

// Envelope mirrors the server-side wire protocol (server/core/transport/protocol.go).
type Envelope struct {
	Type    string `json:"type"`
	ID      string `json:"id,omitempty"`
	Command string `json:"command,omitempty"`
	Payload any    `json:"payload,omitempty"`
	Status  string `json:"status,omitempty"`
	Result  any    `json:"result,omitempty"`
}

const (
	TypeHello            = "hello"
	TypeHeartbeat        = "heartbeat"
	TypeCommandResult    = "command_result"
	TypeCommand          = "command"
	TypeInventory        = "inventory"           // agent -> server: collection result
	TypeInventoryCollect = "inventory.collect"   // server -> agent: collect now
	TypeTermData         = "term.data"           // Phase 5: interactive terminal data stream
	TypeTermClose        = "term.close"          // Phase 5: interactive terminal session closed

	StatusDone   = "done"
	StatusFailed = "failed"
)
