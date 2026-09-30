package protocol

import (
	"encoding/json"
	"testing"
)

// The agent and the server ship as separate binaries with no shared type
// system. These tests exist so that a change to one side which breaks the other
// is a failure here rather than a command the agent silently drops in the
// field, where it is indistinguishable from an idle endpoint.

// TestEnvelopeRoundTripsEveryField pins the wire shape. A field added on one
// side and not the other shows up here as a key the other side cannot read.
func TestEnvelopeRoundTripsEveryField(t *testing.T) {
	original := Envelope{
		Type:    TypeCommand,
		ID:      "cmd-1",
		Command: "ping",
		Payload: map[string]any{"k": "v"},
		Status:  StatusPending,
		Result:  "ok",
		TS:      "2026-01-01T00:00:00Z",
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	// A consumer on the other side of the socket decodes into a struct of its
	// own. Decoding into Envelope alone would pass even if the tags disagreed,
	// because both sides would be the same type.
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("decode into map: %v", err)
	}

	for _, key := range []string{"type", "id", "command", "payload", "status", "result", "ts"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("encoded envelope has no %q key — the peer decodes it and gets an empty value: %s", key, encoded)
		}
	}
}

// TestOmitEmptyFieldsWired documents that a partial envelope stays small. The
// agent sends a bare heartbeat on a 10k-endpoint fleet every 20 seconds; keys
// that are present-but-empty there are a measurable constant cost.
func TestOmitEmptyFieldsWired(t *testing.T) {
	encoded, err := json.Marshal(Envelope{Type: TypeHeartbeat})
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}
	if got, want := string(encoded), `{"type":"heartbeat"}`; got != want {
		t.Errorf("heartbeat encoded as %s, want %s", got, want)
	}
}

// TestTypeAndStatusValuesAreStable guards the literal strings. They are the
// contract: the agent's switch and the server's dispatch both compare against
// these, and a value that changes under a matching pair of names compiles
// perfectly and then never matches again.
func TestTypeAndStatusValuesAreStable(t *testing.T) {
	types := map[string]string{
		"TypeHello":            TypeHello,
		"TypeHeartbeat":        TypeHeartbeat,
		"TypeCommandResult":    TypeCommandResult,
		"TypeInventory":        TypeInventory,
		"TypeTermData":         TypeTermData,
		"TypeTermClose":        TypeTermClose,
		"TypeCommand":          TypeCommand,
		"TypeInventoryCollect": TypeInventoryCollect,
	}
	wantTypes := map[string]string{
		"TypeHello":            "hello",
		"TypeHeartbeat":        "heartbeat",
		"TypeCommandResult":    "command_result",
		"TypeInventory":        "inventory",
		"TypeTermData":         "term.data",
		"TypeTermClose":        "term.close",
		"TypeCommand":          "command",
		"TypeInventoryCollect": "inventory.collect",
	}
	for name, got := range types {
		if want := wantTypes[name]; got != want {
			t.Errorf("%s = %q, want %q — both peers compile against this literal, and a change here is a silent protocol break", name, got, want)
		}
	}

	statuses := map[string]string{
		"StatusDone":    StatusDone,
		"StatusFailed":  StatusFailed,
		"StatusPending": StatusPending,
		"StatusSent":    StatusSent,
	}
	wantStatuses := map[string]string{"StatusDone": "done", "StatusFailed": "failed", "StatusPending": "pending", "StatusSent": "sent"}
	for name, got := range statuses {
		if want := wantStatuses[name]; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// TestTypeValuesAreUnique catches a copy-paste that gives two names the same
// literal. A switch keyed on the value would then have two cases that can never
// both be reached, and the second one is silently dead.
func TestTypeValuesAreUnique(t *testing.T) {
	seen := map[string]string{}
	for name, value := range map[string]string{
		"TypeHello":            TypeHello,
		"TypeHeartbeat":        TypeHeartbeat,
		"TypeCommandResult":    TypeCommandResult,
		"TypeInventory":        TypeInventory,
		"TypeTermData":         TypeTermData,
		"TypeTermClose":        TypeTermClose,
		"TypeCommand":          TypeCommand,
		"TypeInventoryCollect": TypeInventoryCollect,
	} {
		if other, dup := seen[value]; dup {
			t.Errorf("%s and %s share the value %q — one of the two dispatch cases is dead", name, other, value)
		}
		seen[value] = name
	}
}
