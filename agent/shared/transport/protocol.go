package transport

import "github.com/henokakunemail-stack/Endpoint-Manager/protocol"

// The wire contract lives in one package, shared with the server. Aliasing keeps
// every transport.Envelope / transport.TypeHello reference in the agent
// compiling unchanged while making a divergence between the two sides a
// compile error instead of a command the agent silently drops. The previous
// copy of these declarations drifted: it carried no TS field and only two of
// the four status values the server defines.
type Envelope = protocol.Envelope

const (
	TypeHello            = protocol.TypeHello
	TypeHeartbeat        = protocol.TypeHeartbeat
	TypeCommandResult    = protocol.TypeCommandResult
	TypeCommand          = protocol.TypeCommand
	TypeInventory        = protocol.TypeInventory
	TypeInventoryCollect = protocol.TypeInventoryCollect
	TypeTermData         = protocol.TypeTermData
	TypeTermClose        = protocol.TypeTermClose

	StatusDone   = protocol.StatusDone
	StatusFailed = protocol.StatusFailed
)
