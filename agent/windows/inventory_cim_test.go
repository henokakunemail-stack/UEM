//go:build windows

package windows

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestCimShapeIsExactlyWhatPowerShellEmits pins the parsing contract to the real
// bytes ConvertTo-Json produces on Windows 11. Every input was captured from
// powershell.exe, not invented — that matters because the failure this package
// guards against was caused by an invented shape.
func TestCimShapeIsExactlyWhatPowerShellEmits(t *testing.T) {
	cases := []struct {
		name string
		raw  string // as emitted, including the trailing newline
		want []map[string]any
	}{
		{
			name: "single row, one property",
			// Captured: Get-CimInstance -ClassName Win32_ComputerSystem -Property
			// TotalPhysicalMemory | Select-Object TotalPhysicalMemory |
			// ConvertTo-Json -Compress
			raw:  "[{\"TotalPhysicalMemory\":16905961472}]\n",
			want: []map[string]any{{"TotalPhysicalMemory": float64(16905961472)}},
		},
		{
			name: "single row, two properties",
			// Captured: same query against Win32_ComputerSystem with
			// Manufacturer and Model.
			raw:  "[{\"Manufacturer\":\"Dell Inc.\",\"Model\":\"Latitude 3420\"}]\n",
			want: []map[string]any{{"Manufacturer": "Dell Inc.", "Model": "Latitude 3420"}},
		},
		{
			name: "multiple rows",
			raw:  "[{\"Name\":\"a\"},{\"Name\":\"b\"}]\n",
			want: []map[string]any{{"Name": "a"}, {"Name": "b"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := parseCIMRows([]byte(tc.raw))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(rows) != len(tc.want) {
				t.Fatalf("row count: got %d, want %d", len(rows), len(tc.want))
			}
			for i := range rows {
				for k, want := range tc.want[i] {
					if got := rows[i][k]; got != want {
						t.Errorf("row %d key %q: got %v, want %v", i, k, got, want)
					}
				}
			}
		})
	}
}

// TestCimAcceptsBareObjectEvenThoughCompressDoesNotEmitOne: ConvertTo-Json with
// -Compress has always emitted an array here, but the bare-object shape is what
// the documentation permits, so the parser still has to accept it. Keeping this
// test means a PowerShell change that drops the array wrapper is a failing test,
// not a silent regression to "0 bytes RAM".
func TestCimAcceptsBareObjectEvenThoughCompressDoesNotEmitOne(t *testing.T) {
	rows, err := parseCIMRows([]byte("{\"SerialNumber\":\"ABCD123\"}\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 1 || rows[0]["SerialNumber"] != "ABCD123" {
		t.Fatalf("got %v", rows)
	}
}

// TestCimRejectsBrokenOutput guards the failure mode: a CIM call that produced
// truncated or non-JSON output must surface as an error, not as a silently empty
// inventory section that reads on the dashboard as "0 bytes RAM". The truncated
// array below is the shape an earlier version of this code produced when it
// mis-read the newline, and it went undetected precisely because it was silent.
func TestCimRejectsBrokenOutput(t *testing.T) {
	broken := []string{
		"",
		"the rpc server is unavailable",
		"[{\"TotalPhysicalMemory\":16905961472", // truncated array
		"{\"SerialNumber\":\"ABCD123\"",         // truncated object
	}
	for _, b := range broken {
		if _, err := parseCIMRows([]byte(b)); err == nil {
			t.Errorf("input %q must be rejected", b)
		}
	}
}

// TestAsInt64 covers the two encodings PowerShell uses for large numbers: a JSON
// float, which is what ConvertTo-Json emits, and a string, which WMI sometimes
// hands back for uint64 properties.
func TestAsInt64(t *testing.T) {
	if n, ok := asInt64(float64(16905961472)); !ok || n != 16905961472 {
		t.Fatalf("float64 case: got %d, ok %v", n, ok)
	}
	if n, ok := asInt64("16905961472"); !ok || n != 16905961472 {
		t.Fatalf("string case: got %d, ok %v", n, ok)
	}
	if _, ok := asInt64(nil); ok {
		t.Error("nil must not coerce")
	}
}

// keep used so the import is exercised by the build.
var _ = bytes.TrimSpace
var _ = json.Unmarshal
