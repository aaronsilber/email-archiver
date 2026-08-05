package jmap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client is a sequential JMAP client. Exactly one request is in flight at a
// time: with hundreds of messages moved per round trip there is nothing to
// gain from concurrency, and serial requests are the simplest way to stay
// inside the server's rate limits.
type Client struct {
	httpClient *http.Client
	sessionURL string
	token      string

	// MaxAttempts bounds retries of a single request (1 = no retry).
	MaxAttempts int
	// BaseBackoff is the first retry delay; it doubles each attempt.
	BaseBackoff time.Duration
	// MaxBackoff caps the computed delay. A server-sent Retry-After wins over
	// both, since the server knows better.
	MaxBackoff time.Duration

	// sleep is swappable so tests do not actually wait.
	sleep func(context.Context, time.Duration) error
	// Trace, when set, receives one line per HTTP request for --verbose.
	Trace func(format string, args ...any)
}

// NewClient builds a client for the given session URL and bearer token.
func NewClient(sessionURL, token string) *Client {
	return &Client{
		httpClient:  &http.Client{Timeout: 2 * time.Minute},
		sessionURL:  sessionURL,
		token:       token,
		MaxAttempts: 6,
		BaseBackoff: time.Second,
		MaxBackoff:  32 * time.Second,
		sleep:       sleepCtx,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Invocation is a JMAP method call, serialized as the 3-element array
// ["Method/name", {args}, "callId"].
type Invocation struct {
	Name   string
	Args   any
	CallID string
}

// MarshalJSON renders the invocation as JMAP's positional triple.
func (i Invocation) MarshalJSON() ([]byte, error) {
	return json.Marshal([3]any{i.Name, i.Args, i.CallID})
}

// UnmarshalJSON reads JMAP's positional triple, leaving Args as raw JSON so
// each caller can decode into its own type.
func (i *Invocation) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if len(raw) != 3 {
		return fmt.Errorf("method response has %d elements, want 3", len(raw))
	}
	if err := json.Unmarshal(raw[0], &i.Name); err != nil {
		return err
	}
	if err := json.Unmarshal(raw[2], &i.CallID); err != nil {
		return err
	}
	i.Args = json.RawMessage(raw[1])
	return nil
}

// RawArgs returns the undecoded argument object of a response invocation.
func (i Invocation) RawArgs() json.RawMessage {
	if raw, ok := i.Args.(json.RawMessage); ok {
		return raw
	}
	return nil
}

type request struct {
	Using       []string     `json:"using"`
	MethodCalls []Invocation `json:"methodCalls"`
}

type response struct {
	MethodResponses []Invocation `json:"methodResponses"`
	SessionState    string       `json:"sessionState"`
}

// MethodError is a JMAP method-level error (a response named "error").
type MethodError struct {
	CallID      string
	Type        string `json:"type"`
	Description string `json:"description"`
}

func (e *MethodError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("JMAP method %s failed: %s (%s)", e.CallID, e.Description, e.Type)
	}
	return fmt.Sprintf("JMAP method %s failed: %s", e.CallID, e.Type)
}

// RequestError is a request-level failure: an HTTP status outside 2xx, or a
// JMAP problem-details document.
type RequestError struct {
	StatusCode int
	Type       string `json:"type"`
	Detail     string `json:"detail"`
	Limit      string `json:"limit"`
	Body       string `json:"-"`
}

func (e *RequestError) Error() string {
	switch {
	case e.StatusCode == http.StatusUnauthorized:
		return "Fastmail rejected the API token (HTTP 401) — check that it is current and has the mail read+write scope"
	case e.Detail != "":
		return fmt.Sprintf("JMAP request failed (HTTP %d): %s", e.StatusCode, e.Detail)
	case e.Type != "":
		return fmt.Sprintf("JMAP request failed (HTTP %d): %s", e.StatusCode, e.Type)
	default:
		return fmt.Sprintf("JMAP request failed (HTTP %d): %s", e.StatusCode, truncate(e.Body, 200))
	}
}

// Fatal reports whether retrying could ever help.
func (e *RequestError) Fatal() bool {
	switch e.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return true
	}
	return false
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Do sends one JMAP request containing the given method calls and returns the
// method responses in order. A method-level error aborts with *MethodError.
func (c *Client) Do(ctx context.Context, apiURL string, calls []Invocation) ([]Invocation, error) {
	payload, err := json.Marshal(request{
		Using:       []string{CapCore, CapMail},
		MethodCalls: calls,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	body, err := c.doWithRetry(ctx, req, payload)
	if err != nil {
		return nil, err
	}

	var resp response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing JMAP response: %w", err)
	}
	for _, inv := range resp.MethodResponses {
		if inv.Name == "error" {
			me := &MethodError{CallID: inv.CallID}
			_ = json.Unmarshal(inv.RawArgs(), me)
			return nil, me
		}
	}
	if len(resp.MethodResponses) != len(calls) {
		return nil, fmt.Errorf("JMAP returned %d method responses for %d calls", len(resp.MethodResponses), len(calls))
	}
	return resp.MethodResponses, nil
}

// doWithRetry performs an HTTP request, retrying on 429, 5xx, and transport
// errors. payload is resent on each attempt; pass nil for bodyless requests.
func (c *Client) doWithRetry(ctx context.Context, req *http.Request, payload []byte) ([]byte, error) {
	attempts := c.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if payload != nil {
			req.Body = io.NopCloser(bytes.NewReader(payload))
			req.ContentLength = int64(len(payload))
			req.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(payload)), nil
			}
		}

		body, retryAfter, err := c.attempt(req)
		if err == nil {
			return body, nil
		}
		lastErr = err

		var reqErr *RequestError
		if errors.As(err, &reqErr) && reqErr.Fatal() {
			return nil, err
		}
		if attempt == attempts {
			break
		}

		delay := c.backoff(attempt, retryAfter)
		if c.Trace != nil {
			c.Trace("retrying in %s after: %v", delay.Round(time.Millisecond), err)
		}
		if err := c.sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("after %d attempts: %w", attempts, lastErr)
}

// attempt makes a single HTTP call. The returned duration is a server-supplied
// Retry-After, or zero.
func (c *Client) attempt(req *http.Request) ([]byte, time.Duration, error) {
	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if c.Trace != nil {
		c.Trace("%s %s → %d (%s, %d bytes)", req.Method, req.URL.Path, resp.StatusCode, time.Since(start).Round(time.Millisecond), len(body))
	}
	if readErr != nil {
		return nil, 0, fmt.Errorf("reading response body: %w", readErr)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return body, 0, nil
	}

	reqErr := &RequestError{StatusCode: resp.StatusCode, Body: string(body)}
	// Problem details are advisory; a malformed one must not mask the status.
	_ = json.Unmarshal(body, reqErr)
	return nil, parseRetryAfter(resp.Header.Get("Retry-After")), reqErr
}

// parseRetryAfter handles both the delay-seconds and HTTP-date forms.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// backoff returns the delay before the next attempt: the server's Retry-After
// if it sent one, otherwise exponential growth with jitter.
func (c *Client) backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	base := c.BaseBackoff
	if base <= 0 {
		base = time.Second
	}
	d := base << (attempt - 1)
	if max := c.MaxBackoff; max > 0 && d > max {
		d = max
	}
	// Jitter spreads retries so a batch of failures does not resynchronize.
	return d + rand.N(d/2+1)
}
