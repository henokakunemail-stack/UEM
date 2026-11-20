package inventory

import "testing"

func TestParseProcStatBtime(t *testing.T) {
	stat := "cpu  1 2 3\nintr 5\nctxt 9\nbtime 1700000000\nprocesses 4\n"
	if got := ParseProcStatBtime(stat); got != "2023-11-14T22:13:20Z" {
		t.Errorf("got %q", got)
	}
	if got := ParseProcStatBtime("cpu 1\n"); got != "" {
		t.Errorf("missing btime must be empty, got %q", got)
	}
}

func TestParseMacBoottime(t *testing.T) {
	if got := ParseMacBoottime("{ sec = 1700000000, usec = 123 } Tue Nov 14 22:13:20 2023\n"); got != "2023-11-14T22:13:20Z" {
		t.Errorf("got %q", got)
	}
	if got := ParseMacBoottime("garbage"); got != "" {
		t.Errorf("got %q", got)
	}
}
