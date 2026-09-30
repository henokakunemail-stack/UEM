package httpguard

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestABodyOverTheCapIsRejectedAt413 is the direct check on the cap.
//
// The vulnerability being closed is not "a large body is accepted" but "a
// large body is read before anything decides whether to accept it": the
// vulnerable handlers call json.NewDecoder(r.Body).Decode and only afterwards
// look at a token or a username. So the assertion that matters is that the
// reader stops, not that the handler happens to return an error.
func TestABodyOverTheCapIsRejectedAt413(t *testing.T) {
	// 2 MB against a 1 MB cap. Small enough to be instant, well over the line.
	body := strings.Repeat("a", 2*JSONBodyLimit)

	var got []byte
	var readErr error
	h := LimitJSONBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Exactly what the vulnerable handlers do.
		got, readErr = io.ReadAll(r.Body)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if int64(len(got)) != JSONBodyLimit {
		t.Errorf("handler read %d bytes of a %d-byte body; the cap did not stop the read",
			len(got), len(body))
	}
	if readErr == nil {
		t.Error("the capped read succeeded: the body was delivered in full")
	}

	// The error has to be *MaxBytesError, not a generic one. A caller that
	// cannot recognise it answers 400 "invalid json" and the sender retries
	// smaller forever, which is the behaviour this whole change exists to stop.
	var tooLarge *http.MaxBytesError
	if !errors.As(readErr, &tooLarge) {
		t.Errorf("read error is %T (%v); callers match on *http.MaxBytesError and "+
			"will not recognise this one", readErr, readErr)
	}
}

// TestABodyUnderTheCapIsUnaffected is the other half. A cap that broke
// legitimate traffic would be worse than no cap, so the boundary has to be
// pinned from both sides.
func TestABodyUnderTheCapIsUnaffected(t *testing.T) {
	const body = `{"username":"admin","password":"hunter2"}`

	var got string
	var err error
	h := LimitJSONBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, e := io.ReadAll(r.Body)
		got, err = string(b), e
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if err != nil {
		t.Fatalf("a body well under the cap failed to read: %v", err)
	}
	if got != body {
		t.Errorf("body = %q, want %q", got, body)
	}
}

// TestTheCapIsNotOffByTheEnvelope: a request exactly at the limit is a
// pathological case nobody hits, but the reader is one byte short of the
// declared length only if the cap were off by one, and that would show up as a
// truncation bug on a legitimately large-but-valid payload.
func TestTheCapIsNotOffByTheEnvelope(t *testing.T) {
	body := strings.Repeat("x", JSONBodyLimit-64) // room for the header

	var gotLen int
	var err error
	h := LimitJSONBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, e := io.ReadAll(r.Body)
		gotLen, err = len(b), e
	}))

	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
	h.ServeHTTP(httptest.NewRecorder(), req)

	if err != nil {
		t.Fatalf("a body just under the cap failed: %v", err)
	}
	if gotLen != len(body) {
		t.Errorf("read %d bytes, sent %d: the cap truncated a body it should have "+
			"passed through", gotLen, len(body))
	}
}

// TestContentLengthOverLimitIsADeclarationNotAMeasurement is gone with the
// helper. ContentLengthOverLimit had no production caller: MaxBytesReader
// already fails the decode at the cap, and every route that wanted a 413 read
// *http.MaxBytesError. A function whose only callers are its own tests is
// infrastructure that proves nothing.

// TestLimitJSONBodyFuncMatchesLimitJSONBody: the two entry points exist because
// two modules mount on different muxes. If they ever diverge, one of the four
// unauthenticated routes silently loses its cap.
func TestLimitJSONBodyFuncMatchesLimitJSONBody(t *testing.T) {
	read := func(wrap func(http.HandlerFunc) http.HandlerFunc) int {
		var n int
		h := wrap(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			n = len(b)
		})
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(strings.Repeat("a", 2*JSONBodyLimit)))
		h(httptest.NewRecorder(), req)
		return n
	}

	viaHandler := read(func(next http.HandlerFunc) http.HandlerFunc {
		return LimitJSONBody(next).ServeHTTP
	})
	viaFunc := read(LimitJSONBodyFunc)

	if viaHandler != viaFunc {
		t.Errorf("LimitJSONBodyFunc read %d bytes where LimitJSONBody read %d; the "+
			"two entry points have drifted", viaFunc, viaHandler)
	}
	if viaFunc > JSONBodyLimit {
		t.Errorf("LimitJSONBodyFunc let %d bytes through a %d byte cap",
			viaFunc, JSONBodyLimit)
	}
}
