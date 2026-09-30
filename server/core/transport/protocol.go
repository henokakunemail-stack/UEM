package transport

import "github.com/henokakunemail-stack/Endpoint-Manager/protocol"

// The wire contract lives in one package. These aliases keep every existing
// transport.Envelope / transport.TypeHello reference in the server compiling
// and reading the way it always has, while the definition itself can only be
// changed in one place. A second declaration here would be a redeclaration
// compile error rather than a silent runtime mismatch.
type Envelope = protocol.Envelope

const (
	// agent -> server
	TypeHello         = protocol.TypeHello
	TypeHeartbeat     = protocol.TypeHeartbeat
	TypeCommandResult = protocol.TypeCommandResult
	TypeInventory     = protocol.TypeInventory
	TypeTermData      = protocol.TypeTermData
	TypeTermClose     = protocol.TypeTermClose

	// server -> agent
	TypeCommand          = protocol.TypeCommand
	TypeInventoryCollect = protocol.TypeInventoryCollect
)

const (
	StatusDone    = protocol.StatusDone
	StatusFailed  = protocol.StatusFailed
	StatusPending = protocol.StatusPending
	StatusSent    = protocol.StatusSent
)
