// Package jmap is a minimal JMAP client covering only what email-archiver
// needs: fetching the session document, listing mailboxes, querying emails,
// and patching an email's mailboxIds. JMAP is plain JSON over HTTPS, so this
// is stdlib net/http and encoding/json throughout.
package jmap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Capability URIs used in every request.
const (
	CapCore = "urn:ietf:params:jmap:core"
	CapMail = "urn:ietf:params:jmap:mail"
)

// DefaultSessionURL is Fastmail's JMAP session endpoint.
const DefaultSessionURL = "https://api.fastmail.com/jmap/session"

// Session is the subset of the JMAP session document this tool uses.
type Session struct {
	APIURL          string                 `json:"apiUrl"`
	Capabilities    map[string]coreCapJSON `json:"capabilities"`
	PrimaryAccounts map[string]string      `json:"primaryAccounts"`
	Username        string                 `json:"username"`

	// AccountID is the mail account resolved from PrimaryAccounts.
	AccountID string `json:"-"`
	// Limits come from the core capability, with fallbacks applied.
	Limits Limits `json:"-"`
}

type coreCapJSON struct {
	MaxObjectsInGet   int `json:"maxObjectsInGet"`
	MaxObjectsInSet   int `json:"maxObjectsInSet"`
	MaxCallsInRequest int `json:"maxCallsInRequest"`
	MaxSizeRequest    int `json:"maxSizeRequest"`
}

// Limits are the server-advertised request bounds, with sane defaults when the
// server omits them.
type Limits struct {
	MaxObjectsInGet   int
	MaxObjectsInSet   int
	MaxCallsInRequest int
	MaxSizeRequest    int
}

// Fallbacks used when the server advertises no value. Conservative on purpose:
// exceeding a real limit costs a failed round trip.
const (
	defaultMaxObjectsInGet   = 500
	defaultMaxObjectsInSet   = 500
	defaultMaxCallsInRequest = 16
	defaultMaxSizeRequest    = 10 << 20
)

// FetchSession retrieves and validates the session document.
func (c *Client) FetchSession(ctx context.Context) (*Session, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.sessionURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	body, err := c.doWithRetry(ctx, req, nil)
	if err != nil {
		return nil, fmt.Errorf("fetching JMAP session: %w", err)
	}

	var s Session
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("parsing JMAP session document: %w", err)
	}
	if s.APIURL == "" {
		return nil, fmt.Errorf("JMAP session document has no apiUrl")
	}
	s.AccountID = s.PrimaryAccounts[CapMail]
	if s.AccountID == "" {
		return nil, fmt.Errorf("account has no primary mail account (is the token missing the mail scope?)")
	}
	s.Limits = limitsFrom(s.Capabilities[CapCore])
	return &s, nil
}

func limitsFrom(c coreCapJSON) Limits {
	pick := func(v, fallback int) int {
		if v > 0 {
			return v
		}
		return fallback
	}
	return Limits{
		MaxObjectsInGet:   pick(c.MaxObjectsInGet, defaultMaxObjectsInGet),
		MaxObjectsInSet:   pick(c.MaxObjectsInSet, defaultMaxObjectsInSet),
		MaxCallsInRequest: pick(c.MaxCallsInRequest, defaultMaxCallsInRequest),
		MaxSizeRequest:    pick(c.MaxSizeRequest, defaultMaxSizeRequest),
	}
}
