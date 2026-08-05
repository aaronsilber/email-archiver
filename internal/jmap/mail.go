package jmap

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// UTCDate is a JMAP date: RFC3339, always UTC, second precision.
type UTCDate time.Time

// MarshalJSON renders the date in JMAP's UTCDate form.
func (d UTCDate) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(d).UTC().Truncate(time.Second).Format("2006-01-02T15:04:05Z"))
}

// String renders the date the same way the wire does, for log messages.
func (d UTCDate) String() string {
	return time.Time(d).UTC().Truncate(time.Second).Format("2006-01-02T15:04:05Z")
}

// Mailbox roles this tool cares about. Fastmail sets these on the special
// mailboxes; user-created mailboxes have an empty role.
const (
	RoleInbox   = "inbox"
	RoleArchive = "archive"
	RoleTrash   = "trash"
	RoleJunk    = "junk"
	RoleDrafts  = "drafts"
	RoleSent    = "sent"
)

// Mailbox is the subset of the JMAP Mailbox object this tool reads.
type Mailbox struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	ParentID    string `json:"parentId"`
	TotalEmails int    `json:"totalEmails"`
}

// FilterCondition is a JMAP FilterCondition for Email/query. Only the fields
// this tool uses are present; all are omitted when empty.
type FilterCondition struct {
	InMailbox  string   `json:"inMailbox,omitempty"`
	Before     *UTCDate `json:"before,omitempty"`
	After      *UTCDate `json:"after,omitempty"`
	HasKeyword string   `json:"hasKeyword,omitempty"`
	NotKeyword string   `json:"notKeyword,omitempty"`
}

// FilterOperator combines conditions, e.g. {"operator":"AND", ...}.
type FilterOperator struct {
	Operator   string `json:"operator"`
	Conditions []any  `json:"conditions"`
}

// Comparator is a JMAP sort term.
type Comparator struct {
	Property    string `json:"property"`
	IsAscending bool   `json:"isAscending"`
}

// GetMailboxes fetches every mailbox in the account.
func (c *Client) GetMailboxes(ctx context.Context, s *Session) ([]Mailbox, error) {
	calls := []Invocation{{
		Name:   "Mailbox/get",
		CallID: "m0",
		Args: map[string]any{
			"accountId":  s.AccountID,
			"ids":        nil,
			"properties": []string{"id", "name", "role", "parentId", "totalEmails"},
		},
	}}
	responses, err := c.Do(ctx, s.APIURL, calls)
	if err != nil {
		return nil, err
	}

	var out struct {
		List []Mailbox `json:"list"`
	}
	if err := json.Unmarshal(responses[0].RawArgs(), &out); err != nil {
		return nil, fmt.Errorf("parsing Mailbox/get response: %w", err)
	}
	sort.Slice(out.List, func(i, j int) bool {
		return strings.ToLower(out.List[i].Name) < strings.ToLower(out.List[j].Name)
	})
	return out.List, nil
}

// QueryResult is one Email/query response.
type QueryResult struct {
	IDs   []string `json:"ids"`
	Total int      `json:"total"`
	// TotalKnown is false when the server omitted total (calculateTotal off).
	TotalKnown bool `json:"-"`
}

// QueryEmails runs a single Email/query.
func (c *Client) QueryEmails(ctx context.Context, s *Session, filter any, limit int, calculateTotal bool) (QueryResult, error) {
	results, err := c.QueryEmailsBatch(ctx, s, []any{filter}, limit, calculateTotal)
	if err != nil {
		return QueryResult{}, err
	}
	return results[0], nil
}

// QueryEmailsBatch runs one Email/query per filter in a single HTTP request —
// this is how the pre-run count table is produced in one round trip. The
// caller must keep len(filters) within Session.Limits.MaxCallsInRequest.
func (c *Client) QueryEmailsBatch(ctx context.Context, s *Session, filters []any, limit int, calculateTotal bool) ([]QueryResult, error) {
	calls := make([]Invocation, 0, len(filters))
	for i, f := range filters {
		calls = append(calls, Invocation{
			Name:   "Email/query",
			CallID: fmt.Sprintf("q%d", i),
			Args: map[string]any{
				"accountId": s.AccountID,
				"filter":    f,
				// Oldest first: a partial run always archives the oldest mail,
				// which is the mail the user cares least about seeing again.
				"sort":           []Comparator{{Property: "receivedAt", IsAscending: true}},
				"position":       0,
				"limit":          limit,
				"calculateTotal": calculateTotal,
			},
		})
	}

	responses, err := c.Do(ctx, s.APIURL, calls)
	if err != nil {
		return nil, err
	}

	out := make([]QueryResult, len(responses))
	for i, resp := range responses {
		var parsed struct {
			IDs   []string `json:"ids"`
			Total *int     `json:"total"`
		}
		if err := json.Unmarshal(resp.RawArgs(), &parsed); err != nil {
			return nil, fmt.Errorf("parsing Email/query response: %w", err)
		}
		out[i] = QueryResult{IDs: parsed.IDs}
		if parsed.Total != nil {
			out[i].Total = *parsed.Total
			out[i].TotalKnown = true
		}
	}
	return out, nil
}

// SetError is a per-message failure from Email/set.
type SetError struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

// MoveResult reports the outcome of one Email/set batch.
type MoveResult struct {
	Updated    []string
	NotUpdated map[string]SetError
}

// MoveEmails moves messages out of srcMailboxID and into dstMailboxID.
//
// The update is a JSON Patch on mailboxIds and nothing else. Two consequences
// matter: any *other* mailbox the message belongs to (a user label, say) is
// left in place, and because keywords are never sent, $seen and $flagged are
// untouched. receivedAt is server-immutable. That is what makes this
// indistinguishable from clicking Archive in the web UI.
func (c *Client) MoveEmails(ctx context.Context, s *Session, ids []string, srcMailboxID, dstMailboxID string) (MoveResult, error) {
	if len(ids) == 0 {
		return MoveResult{NotUpdated: map[string]SetError{}}, nil
	}

	update := make(map[string]any, len(ids))
	for _, id := range ids {
		update[id] = map[string]any{
			"mailboxIds/" + srcMailboxID: nil,
			"mailboxIds/" + dstMailboxID: true,
		}
	}

	calls := []Invocation{{
		Name:   "Email/set",
		CallID: "s0",
		Args: map[string]any{
			"accountId": s.AccountID,
			"update":    update,
			// No ifInState: mail arriving mid-run would abort an otherwise
			// fine batch, and per-message errors already cover real conflicts.
		},
	}}

	responses, err := c.Do(ctx, s.APIURL, calls)
	if err != nil {
		return MoveResult{}, err
	}

	var parsed struct {
		Updated    map[string]json.RawMessage `json:"updated"`
		NotUpdated map[string]SetError        `json:"notUpdated"`
	}
	if err := json.Unmarshal(responses[0].RawArgs(), &parsed); err != nil {
		return MoveResult{}, fmt.Errorf("parsing Email/set response: %w", err)
	}

	result := MoveResult{
		Updated:    make([]string, 0, len(parsed.Updated)),
		NotUpdated: parsed.NotUpdated,
	}
	if result.NotUpdated == nil {
		result.NotUpdated = map[string]SetError{}
	}
	// Preserve the caller's order so progress output tracks the query order.
	for _, id := range ids {
		if _, ok := parsed.Updated[id]; ok {
			result.Updated = append(result.Updated, id)
		}
	}
	return result, nil
}
