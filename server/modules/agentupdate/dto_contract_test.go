package agentupdate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// The console reads these DTOs from fixtures in web-console/src/test/contracts,
// and its own tests render them. This test holds the Go side to the same
// files: every fixture key must be a field here, and every field here must be
// in the fixture. The console's campaign type once said *_tasks while this
// struct said *_devices, so campaign progress never rendered and nothing
// failed.
func TestConsoleContractFixturesMatchTheDTOs(t *testing.T) {
	cases := map[string]any{
		"update-campaign.json":      &UpdateCampaignDTO{},
		"campaign-log-item.json":    &CampaignLogItem{},
		"campaign-task-detail.json": &CampaignTaskDetail{},
	}
	for file, target := range cases {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "web-console", "src", "test", "contracts", file))
			if err != nil {
				t.Fatal(err)
			}
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(target); err != nil {
				t.Fatalf("the console expects a field the server does not send: %v", err)
			}
			back, err := json.Marshal(target)
			if err != nil {
				t.Fatal(err)
			}
			want, got := keys(t, raw), keys(t, back)
			if len(want) != len(got) {
				t.Fatalf("server sends %v, console fixture has %v", got, want)
			}
			for i := range want {
				if want[i] != got[i] {
					t.Fatalf("server sends %v, console fixture has %v", got, want)
				}
			}
		})
	}
}

func keys(t *testing.T, raw []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
