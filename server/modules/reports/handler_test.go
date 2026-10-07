package reports

import (
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// writeCSV encodes the whole report before committing any header, so an
// encoding failure is a 500 rather than a 200 with a truncated CSV that an
// operator saves believing it is complete.
//
// The failure below is returned by the row emitter, which is how every
// writer.Write error reaches the handler. A content-induced failure is not
// used as the trigger: Go's csv.Writer quotes \r, \n and commas rather than
// rejecting them, and the bytes.Buffer it encodes into never fails, so a row
// emitter returning an error is the only path that can report a problem.
func TestWriteCSVReportsARowErrorAsAFailedDownload(t *testing.T) {
	rec := httptest.NewRecorder()
	writeCSV(rec, "device-inventory-test.csv",
		[]string{"ID", "Hostname"},
		func(writer *csv.Writer) error {
			if err := writer.Write([]string{"dev-1", "good-host"}); err != nil {
				return err
			}
			return http.ErrAbortHandler // a row write failed
		})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500: a failed row must not be served as a "+
			"successful download", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct == "text/csv" {
		t.Errorf("Content-Type is text/csv; the attachment headers must not be set " +
			"when the report never encoded, or the browser still offers a download")
	}
	if strings.Contains(rec.Body.String(), "good-host") {
		t.Errorf("body contains the first row; a partially encoded report must not "+
			"be delivered as though it completed. body = %q", rec.Body.String())
	}
}

// The happy path must still render the full report with headers, since the
// failure handling above is worthless if it also breaks the working case.
func TestWriteCSVServesTheCompleteReportWhenEncodingSucceeds(t *testing.T) {
	rec := httptest.NewRecorder()
	writeCSV(rec, "device-inventory-test.csv",
		[]string{"ID", "Hostname"},
		func(writer *csv.Writer) error {
			return writer.Write([]string{"dev-1", "workstation-01"})
		})

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/csv" {
		t.Errorf("Content-Type = %q, want text/csv", got)
	}
	if disp := rec.Header().Get("Content-Disposition"); !strings.Contains(disp, "device-inventory-test.csv") {
		t.Errorf("Content-Disposition = %q, want the filename", disp)
	}
	body := rec.Body.String()
	for _, want := range []string{"ID,Hostname", "dev-1,workstation-01"} {
		if !strings.Contains(body, want) {
			t.Errorf("body = %q, missing %q", body, want)
		}
	}
}

// Quoting is the encoder's job, and a field containing a comma has to survive it
// -- the report a compliance audit reads cannot have its columns shifted by an
// unquoted comma in, say, a free-text details field.
func TestWriteCSVQuotesFieldsContainingCommas(t *testing.T) {
	rec := httptest.NewRecorder()
	writeCSV(rec, "audit-trail-test.csv",
		[]string{"Actor", "Details"},
		func(writer *csv.Writer) error {
			return writer.Write([]string{"user", "role changed from Viewer, read-only to Admin"})
		})

	if got := rec.Body.String(); !strings.Contains(got, `"role changed from Viewer, read-only to Admin"`) {
		t.Errorf("a comma inside a field must be quoted so the column count holds; "+
			"got %q", got)
	}
}
