package transport

// Wire protocol messages exchanged over the agent WebSocket.
// Minimal in Phase 1; extended by later modules (inventory, patch, remote control).

// Envelope is the generic message envelope. Type selects the concrete shape.
type Envelope struct {
	Type    string `json:"type"`
	ID      string `json:"id,omitempty"`
	Command string `json:"command,omitempty"`
	Payload any    `json:"payload,omitempty"`
	Status  string `json:"status,omitempty"`
	Result  any    `json:"result,omitempty"`
	TS      string `json:"ts,omitempty"`
}

const (
	// agent -> server
	TypeHello         = "hello"          // sent right after connect
	TypeHeartbeat     = "heartbeat"      // periodic keep-alive
	TypeCommandResult = "command_result" // reply to a server-issued command
	TypeInventory     = "inventory"      // Phase 2: collection result
	TypeTermData      = "term.data"      // Phase 5: interactive terminal data stream
	TypeTermClose     = "term.close"     // Phase 5: interactive terminal closed by agent

	// server -> agent
	TypeCommand          = "command"           // ask agent to run something
	TypeInventoryCollect = "inventory.collect" // Phase 2: request collection now
)

const (
	StatusDone    = "done"
	StatusFailed  = "failed"
	StatusPending = "pending"
	StatusSent    = "sent"
)
