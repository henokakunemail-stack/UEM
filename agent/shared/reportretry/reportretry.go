// Package reportretry posts an agent's report to the server, retrying a
// transient failure.
//
// A terminal report the server never receives strands the row it closes: the
// task stays 'dispatched', its parent stays 'running', and the console polls
// both forever. One dropped POST — a server restart, a single 502 from a proxy —
// was enough. The retry is bounded so a report never holds its caller for more
// than about a second.
package reportretry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Attempts is how many times a transient failure is tried in total.
const Attempts = 3

// Backoff is the wait before the given retry (1-based): 100ms, 200ms, ...
func Backoff(retry int) time.Duration {
	return 100 * time.Millisecond << (retry - 1)
}

// PermanentError marks a report the server refused on purpose. A 4xx means the
// same payload will be refused again, and a marshal or request-build failure
// will never succeed on a retry — repeating those only burns time.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// IsPermanent reports whether err is a refusal a retry cannot fix.
func IsPermanent(err error) bool {
	var p *PermanentError
	return errors.As(err, &p)
}

// Post marshals body and posts it to url with the device's credentials,
// retrying a transport error or a 5xx up to Attempts times.
func Post(ctx context.Context, client *http.Client, url, deviceID, secret string, body any) error {
	return Do(ctx, func(ctx context.Context) error {
		return PostOnce(ctx, client, url, deviceID, secret, body)
	})
}

// Do runs once until it succeeds, fails permanently, or runs out of attempts.
func Do(ctx context.Context, once func(context.Context) error) error {
	var last error
	for attempt := 1; attempt <= Attempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(Backoff(attempt - 1)):
			}
		}
		last = once(ctx)
		if last == nil || IsPermanent(last) {
			return last
		}
	}
	return last
}

// PostOnce makes a single attempt. The header names are the ones
// devicemgmt.AuthenticateAgent reads.
func PostOnce(ctx context.Context, client *http.Client, url, deviceID, secret string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return &PermanentError{fmt.Errorf("marshal report: %w", err)}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return &PermanentError{fmt.Errorf("create report request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Device-Id", deviceID)
	req.Header.Set("X-Device-Secret", secret)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send report: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return &PermanentError{fmt.Errorf("report rejected with status %d", resp.StatusCode)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("report rejected with status %d", resp.StatusCode)
	}
	return nil
}
