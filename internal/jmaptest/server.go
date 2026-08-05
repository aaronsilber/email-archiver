// Package jmaptest provides an in-memory fake of the small slice of JMAP that
// email-archiver speaks. It exists so the drain loop, the filter shapes, and
// the exact bytes of an Email/set patch can all be asserted without touching a
// real account.
package jmaptest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"
)

// Message is one email in the fake account.
type Message struct {
	ID         string
	MailboxIDs map[string]bool
	Keywords   map[string]bool
	ReceivedAt time.Time
}

// Mailbox is one mailbox in the fake account.
type Mailbox struct {
	ID       string
	Name     string
	Role     string
	ParentID string
}

// Server is a fake JMAP server backed by httptest.
type Server struct {
	mu sync.Mutex

	Mailboxes []Mailbox
	Messages  map[string]*Message

	// AccountID is reported as the primary mail account.
	AccountID string
	// MaxObjectsInSet and MaxCallsInRequest are advertised in the session.
	MaxObjectsInSet   int
	MaxCallsInRequest int

	// RateLimitOnce makes the next API request return 429 with Retry-After.
	RateLimitTimes int
	// RetryAfter is the header value sent with a 429.
	RetryAfter string
	// Unauthorized makes every request return 401.
	Unauthorized bool
	// FailIDs are message ids Email/set reports in notUpdated instead of
	// moving, simulating a per-message failure.
	FailIDs map[string]string

	// Recorded traffic, for assertions.
	SetRequests   []json.RawMessage // raw `update` argument of each Email/set
	QueryFilters  []json.RawMessage // raw `filter` argument of each Email/query
	RequestCount  int
	AuthHeaders   []string
	httptestSrv   *httptest.Server
	sessionCalled int
}

// New starts a fake server with the given mailboxes and messages.
func New(mailboxes []Mailbox, messages []Message) *Server {
	s := &Server{
		Mailboxes:         mailboxes,
		Messages:          map[string]*Message{},
		AccountID:         "acct-1",
		MaxObjectsInSet:   500,
		MaxCallsInRequest: 16,
		FailIDs:           map[string]string{},
	}
	for _, m := range messages {
		// Deep-copy the maps: callers routinely reuse one fixture slice across
		// subtests, and a shared map would let one test's moves leak into the
		// next.
		copied := &Message{ID: m.ID, ReceivedAt: m.ReceivedAt, MailboxIDs: map[string]bool{}, Keywords: map[string]bool{}}
		for k, v := range m.MailboxIDs {
			copied.MailboxIDs[k] = v
		}
		for k, v := range m.Keywords {
			copied.Keywords[k] = v
		}
		s.Messages[m.ID] = copied
	}
	s.httptestSrv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// Close shuts the server down.
func (s *Server) Close() { s.httptestSrv.Close() }

// SessionURL is the URL to hand to jmap.NewClient.
func (s *Server) SessionURL() string { return s.httptestSrv.URL + "/jmap/session" }

// InMailbox returns the ids of messages currently in a mailbox.
func (s *Server) InMailbox(mailboxID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id, m := range s.Messages {
		if m.MailboxIDs[mailboxID] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// Message returns a copy of one message's state.
func (s *Server) Message(id string) (Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.Messages[id]
	if !ok {
		return Message{}, false
	}
	cp := Message{ID: m.ID, ReceivedAt: m.ReceivedAt, MailboxIDs: map[string]bool{}, Keywords: map[string]bool{}}
	for k, v := range m.MailboxIDs {
		cp.MailboxIDs[k] = v
	}
	for k, v := range m.Keywords {
		cp.Keywords[k] = v
	}
	return cp, true
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.RequestCount++
	s.AuthHeaders = append(s.AuthHeaders, r.Header.Get("Authorization"))
	unauthorized := s.Unauthorized
	rateLimited := s.RateLimitTimes > 0
	if rateLimited {
		s.RateLimitTimes--
	}
	retryAfter := s.RetryAfter
	s.mu.Unlock()

	if unauthorized {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"type":"about:blank","status":401,"detail":"Authorization header not a valid credential"}`)
		return
	}
	if rateLimited {
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"type":"urn:ietf:params:jmap:error:limit","limit":"rateLimit","detail":"Too many requests"}`)
		return
	}

	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/jmap/session") {
		s.writeSession(w)
		return
	}
	s.writeAPI(w, r)
}

func (s *Server) writeSession(w http.ResponseWriter) {
	s.mu.Lock()
	s.sessionCalled++
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"username": "test@example.com",
		"apiUrl":   s.httptestSrv.URL + "/jmap/api/",
		"capabilities": map[string]any{
			"urn:ietf:params:jmap:core": map[string]any{
				"maxObjectsInGet":   500,
				"maxObjectsInSet":   s.MaxObjectsInSet,
				"maxCallsInRequest": s.MaxCallsInRequest,
				"maxSizeRequest":    10 << 20,
			},
			"urn:ietf:params:jmap:mail": map[string]any{},
		},
		"primaryAccounts": map[string]any{
			"urn:ietf:params:jmap:mail": s.AccountID,
		},
	})
}

type apiRequest struct {
	Using       []string              `json:"using"`
	MethodCalls []([]json.RawMessage) `json:"methodCalls"`
}

func (s *Server) writeAPI(w http.ResponseWriter, r *http.Request) {
	var req apiRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"type":"urn:ietf:params:jmap:error:notRequest"}`, http.StatusBadRequest)
		return
	}

	responses := make([]any, 0, len(req.MethodCalls))
	for _, call := range req.MethodCalls {
		if len(call) != 3 {
			http.Error(w, `{"type":"urn:ietf:params:jmap:error:notRequest"}`, http.StatusBadRequest)
			return
		}
		var name, callID string
		_ = json.Unmarshal(call[0], &name)
		_ = json.Unmarshal(call[2], &callID)

		var args any
		var err error
		switch name {
		case "Mailbox/get":
			args = s.mailboxGet()
		case "Email/query":
			args, err = s.emailQuery(call[1])
		case "Email/set":
			args, err = s.emailSet(call[1])
		default:
			responses = append(responses, []any{"error", map[string]any{"type": "unknownMethod"}, callID})
			continue
		}
		if err != nil {
			responses = append(responses, []any{"error", map[string]any{"type": "invalidArguments", "description": err.Error()}, callID})
			continue
		}
		responses = append(responses, []any{name, args, callID})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"methodResponses": responses,
		"sessionState":    "state-1",
	})
}

func (s *Server) mailboxGet() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()

	list := make([]map[string]any, 0, len(s.Mailboxes))
	for _, mb := range s.Mailboxes {
		total := 0
		for _, m := range s.Messages {
			if m.MailboxIDs[mb.ID] {
				total++
			}
		}
		list = append(list, map[string]any{
			"id": mb.ID, "name": mb.Name, "role": nullableRole(mb.Role),
			"parentId": nullableID(mb.ParentID), "totalEmails": total,
		})
	}
	return map[string]any{"accountId": s.AccountID, "state": "mb-1", "list": list, "notFound": []string{}}
}

func nullableRole(role string) any {
	if role == "" {
		return nil
	}
	return role
}

func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// filterNode mirrors both a FilterOperator and a FilterCondition, since JMAP
// allows either at any position in the tree.
type filterNode struct {
	Operator   string       `json:"operator"`
	Conditions []filterNode `json:"conditions"`

	InMailbox  string     `json:"inMailbox"`
	Before     *time.Time `json:"before"`
	After      *time.Time `json:"after"`
	HasKeyword string     `json:"hasKeyword"`
	NotKeyword string     `json:"notKeyword"`
}

func (f filterNode) matches(m *Message) bool {
	if f.Operator != "" {
		switch strings.ToUpper(f.Operator) {
		case "AND":
			for _, c := range f.Conditions {
				if !c.matches(m) {
					return false
				}
			}
			return true
		case "OR":
			for _, c := range f.Conditions {
				if c.matches(m) {
					return true
				}
			}
			return false
		case "NOT":
			for _, c := range f.Conditions {
				if c.matches(m) {
					return false
				}
			}
			return true
		}
	}
	if f.InMailbox != "" && !m.MailboxIDs[f.InMailbox] {
		return false
	}
	if f.Before != nil && !m.ReceivedAt.Before(*f.Before) {
		return false
	}
	if f.After != nil && !m.ReceivedAt.After(*f.After) {
		return false
	}
	if f.HasKeyword != "" && !m.Keywords[f.HasKeyword] {
		return false
	}
	if f.NotKeyword != "" && m.Keywords[f.NotKeyword] {
		return false
	}
	return true
}

func (s *Server) emailQuery(raw json.RawMessage) (map[string]any, error) {
	var args struct {
		Filter         json.RawMessage `json:"filter"`
		Position       int             `json:"position"`
		Limit          *int            `json:"limit"`
		CalculateTotal bool            `json:"calculateTotal"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}

	var filter filterNode
	if err := json.Unmarshal(args.Filter, &filter); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.QueryFilters = append(s.QueryFilters, args.Filter)

	var matched []*Message
	for _, m := range s.Messages {
		if filter.matches(m) {
			matched = append(matched, m)
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].ReceivedAt.Equal(matched[j].ReceivedAt) {
			return matched[i].ID < matched[j].ID
		}
		return matched[i].ReceivedAt.Before(matched[j].ReceivedAt)
	})

	ids := []string{}
	start := min(args.Position, len(matched))
	window := matched[start:]
	if args.Limit != nil && *args.Limit < len(window) {
		window = window[:*args.Limit]
	}
	for _, m := range window {
		ids = append(ids, m.ID)
	}

	out := map[string]any{
		"accountId":  s.AccountID,
		"queryState": "q-1",
		"position":   args.Position,
		"ids":        ids,
	}
	if args.CalculateTotal {
		out["total"] = len(matched)
	}
	return out, nil
}

func (s *Server) emailSet(raw json.RawMessage) (map[string]any, error) {
	var args struct {
		Update json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}

	var update map[string]map[string]json.RawMessage
	if err := json.Unmarshal(args.Update, &update); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.SetRequests = append(s.SetRequests, args.Update)

	if s.MaxObjectsInSet > 0 && len(update) > s.MaxObjectsInSet {
		return nil, fmt.Errorf("update has %d objects, over maxObjectsInSet %d", len(update), s.MaxObjectsInSet)
	}

	updated := map[string]any{}
	notUpdated := map[string]any{}
	for id, patch := range update {
		msg, ok := s.Messages[id]
		if !ok {
			notUpdated[id] = map[string]any{"type": "notFound"}
			continue
		}
		if desc, fail := s.FailIDs[id]; fail {
			notUpdated[id] = map[string]any{"type": "forbidden", "description": desc}
			continue
		}
		if badKey, ok := applyPatch(msg, patch); !ok {
			// Anything other than a mailboxIds patch is a bug in the tool: it
			// would risk changing read state, flags, or the received date.
			notUpdated[id] = map[string]any{"type": "invalidPatch", "description": "unexpected patch key " + badKey}
			continue
		}
		updated[id] = nil
	}

	return map[string]any{
		"accountId":  s.AccountID,
		"oldState":   "e-1",
		"newState":   "e-2",
		"updated":    updated,
		"notUpdated": notUpdated,
	}, nil
}

// applyPatch applies a mailboxIds patch. It rejects the whole patch, naming the
// offending key, if any key touches something other than mailboxIds.
func applyPatch(msg *Message, patch map[string]json.RawMessage) (badKey string, ok bool) {
	for key := range patch {
		if !strings.HasPrefix(key, "mailboxIds/") {
			return key, false
		}
	}
	for key, value := range patch {
		path := strings.TrimPrefix(key, "mailboxIds/")
		if string(value) == "null" {
			delete(msg.MailboxIDs, path)
		} else {
			msg.MailboxIDs[path] = true
		}
	}
	return "", true
}
