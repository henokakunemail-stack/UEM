// Package protocol is the single definition of the agent<->server wire
// contract.
//
// It exists because the two halves of this connection ship as two binaries
// built for different architectures, with no shared type system to catch a
// mismatch. When the envelope and the type constants were declared separately in
// server/core/transport and agent/shared/transport, a renamed type or a field
// added on one side compiled cleanly on both and only failed at runtime — the
// server dispatched a command the agent did not recognise, and the agent
// dropped it silently because the switch had no matching case. A missing
// command looks exactly like an idle endpoint in the console.
//
// Both packages alias these declarations rather than redeclaring them, so the
// contract has one place to be edited and a divergence is a compile error.
package protocol

// Envelope is the generic message envelope exchanged on the agent WebSocket.
// Type selects the concrete shape; the remaining fields are interpreted per
// type, and a field that does not apply to a type is omitted from the wire.
type Envelope struct {
	Type    string `json:"type"`
	ID      string `json:"id,omitempty"`
	Command string `json:"command,omitempty"`
	Payload any    `json:"payload,omitempty"`
	Status  string `json:"status,omitempty"`
	Result  any    `json:"result,omitempty"`
	TS      string `json:"ts,omitempty"`
}

// Message types. The direction is a property of who sends it, not of the value,
// so a constant here is usable from either side.
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

// Result and task states. Each module that tracks its own lifecycle defines its
// own richer set next to it — these are the values that cross the wire or are
// shared by more than one module.
const (
	StatusDone    = "done"
	StatusFailed  = "failed"
	StatusPending = "pending"
	StatusSent    = "sent"
)
