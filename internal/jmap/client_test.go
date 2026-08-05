package jmap_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aaronsilber/email-archiver/internal/jmap"
	"github.com/aaronsilber/email-archiver/internal/jmaptest"
)

func fixture() ([]jmaptest.Mailbox, []jmaptest.Message) {
	return []jmaptest.Mailbox{
			{ID: "mb-inbox", Name: "Inbox", Role: jmap.RoleInbox},
			{ID: "mb-archive", Name: "Archive", Role: jmap.RoleArchive},
		}, []jmaptest.Message{
			{ID: "m-1", MailboxIDs: map[string]bool{"mb-inbox": true}, ReceivedAt: time.Date(2019, 6, 1, 0, 0, 0, 0, time.UTC)},
		}
}

func newClient(srv *jmaptest.Server) *jmap.Client {
	c := jmap.NewClient(srv.SessionURL(), "test-token")
	c.BaseBackoff = time.Millisecond
	c.MaxBackoff = 5 * time.Millisecond
	return c
}

func TestFetchSession(t *testing.T) {
	srv := jmaptest.New(fixture())
	defer srv.Close()

	session, err := newClient(srv).FetchSession(context.Background())
	if err != nil {
		t.Fatalf("FetchSession: %v", err)
	}
	if session.AccountID != "acct-1" {
		t.Errorf("accountId = %q, want acct-1", session.AccountID)
	}
	if session.Limits.MaxObjectsInSet != 500 {
		t.Errorf("maxObjectsInSet = %d, want 500", session.Limits.MaxObjectsInSet)
	}
	if got := srv.AuthHeaders[0]; got != "Bearer test-token" {
		t.Errorf("Authorization = %q, want a bearer token", got)
	}
}

// TestRetriesRateLimit checks the tool backs off rather than hammering.
func TestRetriesRateLimit(t *testing.T) {
	srv := jmaptest.New(fixture())
	defer srv.Close()
	srv.RateLimitTimes = 2
	srv.RetryAfter = "0"

	session, err := newClient(srv).FetchSession(context.Background())
	if err != nil {
		t.Fatalf("FetchSession did not recover from 429: %v", err)
	}
	if session.AccountID == "" {
		t.Error("session came back empty after retries")
	}
	if srv.RequestCount != 3 {
		t.Errorf("made %d requests, want 3 (two rate-limited, one success)", srv.RequestCount)
	}
}

// TestUnauthorizedIsFatal checks a bad token fails immediately instead of
// burning six attempts on an error that can never resolve.
func TestUnauthorizedIsFatal(t *testing.T) {
	srv := jmaptest.New(fixture())
	defer srv.Close()
	srv.Unauthorized = true

	_, err := newClient(srv).FetchSession(context.Background())
	if err == nil {
		t.Fatal("FetchSession succeeded against a 401")
	}
	var reqErr *jmap.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("error is %T, want *jmap.RequestError", err)
	}
	if !strings.Contains(err.Error(), "token") {
		t.Errorf("error %q should point at the API token", err)
	}
	if srv.RequestCount != 1 {
		t.Errorf("made %d requests, want 1 — a 401 must not be retried", srv.RequestCount)
	}
}

func TestRetryGivesUpAndReportsLastError(t *testing.T) {
	srv := jmaptest.New(fixture())
	defer srv.Close()
	srv.RateLimitTimes = 100

	c := newClient(srv)
	c.MaxAttempts = 3
	_, err := c.FetchSession(context.Background())
	if err == nil {
		t.Fatal("FetchSession succeeded against an endless 429")
	}
	if !strings.Contains(err.Error(), "3 attempts") {
		t.Errorf("error %q should say how many attempts were made", err)
	}
}

func TestQueryAndMove(t *testing.T) {
	srv := jmaptest.New(fixture())
	defer srv.Close()
	c := newClient(srv)
	ctx := context.Background()

	session, err := c.FetchSession(ctx)
	if err != nil {
		t.Fatalf("FetchSession: %v", err)
	}
	before := jmap.UTCDate(time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC))
	filter := jmap.FilterOperator{Operator: "AND", Conditions: []any{
		jmap.FilterCondition{InMailbox: "mb-inbox"},
		jmap.FilterCondition{Before: &before},
	}}

	q, err := c.QueryEmails(ctx, session, filter, 10, true)
	if err != nil {
		t.Fatalf("QueryEmails: %v", err)
	}
	if !q.TotalKnown || q.Total != 1 || len(q.IDs) != 1 {
		t.Fatalf("query = %+v, want one match with a known total", q)
	}

	res, err := c.MoveEmails(ctx, session, q.IDs, "mb-inbox", "mb-archive")
	if err != nil {
		t.Fatalf("MoveEmails: %v", err)
	}
	if len(res.Updated) != 1 || len(res.NotUpdated) != 0 {
		t.Fatalf("move result = %+v, want one update and no failures", res)
	}
	if got := srv.InMailbox("mb-archive"); len(got) != 1 {
		t.Errorf("Archive holds %v, want one message", got)
	}
}

func TestMoveEmptyIsNoOp(t *testing.T) {
	srv := jmaptest.New(fixture())
	defer srv.Close()
	c := newClient(srv)
	session, err := c.FetchSession(context.Background())
	if err != nil {
		t.Fatalf("FetchSession: %v", err)
	}

	before := srv.RequestCount
	if _, err := c.MoveEmails(context.Background(), session, nil, "mb-inbox", "mb-archive"); err != nil {
		t.Fatalf("MoveEmails: %v", err)
	}
	if srv.RequestCount != before {
		t.Error("moving zero messages still made an HTTP request")
	}
}

func TestUTCDateFormat(t *testing.T) {
	// JMAP wants a UTC RFC3339 stamp with second precision, and the tool must
	// convert from whatever zone the user's --before parsed in.
	zone := time.FixedZone("UTC-5", -5*3600)
	d := jmap.UTCDate(time.Date(2023, 1, 1, 0, 0, 0, 123456789, zone))
	if got, want := d.String(), "2023-01-01T05:00:00Z"; got != want {
		t.Errorf("UTCDate = %s, want %s", got, want)
	}

	b, err := d.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if got, want := string(b), `"2023-01-01T05:00:00Z"`; got != want {
		t.Errorf("marshalled = %s, want %s", got, want)
	}
}
