package update

import (
	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
)

// ReleaseManifest and SignedManifest are aliased from the shared protocol
// package for the same reason the envelope is: the server records a manifest
// and this agent verifies it, and two declarations would drift apart one field
// at a time, with the divergence surfacing only as a rejected update whose
// signature the operator can see is valid.
type ReleaseManifest = protocol.ReleaseManifest
type SignedManifest = protocol.SignedManifest

// re-export the protocol errors so the agent's callers do not need a second
// import to name a failure this package raised.
var (
	ErrNoSignature  = protocol.ErrNoSignature
	ErrBadSignature = protocol.ErrBadSignature
)
