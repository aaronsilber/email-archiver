package archive_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aaronsilber/email-archiver/internal/archive"
	"github.com/aaronsilber/email-archiver/internal/jmap"
	"github.com/aaronsilber/email-archiver/internal/jmaptest"
)

const (
	inboxID   = "mb-inbox"
	archiveID = "mb-archive"
	trashID   = "mb-trash"
	sentID    = "mb-sent"
	labelID   = "mb-label"
)

func standardMailboxes() []jmaptest.Mailbox {
	return []jmaptest.Mailbox{
		{ID: inboxID, Name: "Inbox", Role: jmap.RoleInbox},
		{ID: archiveID, Name: "Archive", Role: jmap.RoleArchive},
		{ID: trashID, Name: "Trash", Role: jmap.RoleTrash},
		{ID: sentID, Name: "Sent", Role: jmap.RoleSent},
		{ID: labelID, Name: "Receipts"},
	}
}

func at(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 12, 0, 0, 0, time.UTC)
}

// connect starts a fake server and returns a client and session pointed at it.
func connect(t *testing.T, srv *jmaptest.Server) (*jmap.Client, *jmap.Session) {
	t.Helper()
	client := jmap.NewClient(srv.SessionURL(), "test-token")
	client.BaseBackoff = time.Millisecond
	session, err := client.FetchSession(context.Background())
	if err != nil {
		t.Fatalf("FetchSession: %v", err)
	}
	return client, session
}

func resolve(t *testing.T, client *jmap.Client, session *jmap.Session, from ...string) archive.Targets {
	t.Helper()
	list, err := client.GetMailboxes(context.Background(), session)
	if err != nil {
		t.Fatalf("GetMailboxes: %v", err)
	}
	targets, err := archive.Resolve(archive.NewMailboxes(list), from)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return targets
}

// TestRunDrainsAndIsIdempotent is the central test: a mailbox larger than the
// batch size drains completely, and running again moves nothing.
func TestRunDrainsAndIsIdempotent(t *testing.T) {
	var msgs []jmaptest.Message
	for i := range 25 {
		msgs = append(msgs, jmaptest.Message{
			ID:         fmt.Sprintf("old-%02d", i),
			MailboxIDs: map[string]bool{inboxID: true},
			ReceivedAt: at(2020, time.March, 1).Add(time.Duration(i) * time.Hour),
		})
	}
	// Two messages that must not move: one after the cutoff, one already filed.
	msgs = append(msgs,
		jmaptest.Message{ID: "new-1", MailboxIDs: map[string]bool{inboxID: true}, ReceivedAt: at(2024, time.January, 5)},
		jmaptest.Message{ID: "filed-1", MailboxIDs: map[string]bool{archiveID: true}, ReceivedAt: at(2019, time.January, 5)},
	)

	srv := jmaptest.New(standardMailboxes(), msgs)
	defer srv.Close()
	client, session := connect(t, srv)
	targets := resolve(t, client, session)

	opts := archive.Options{Before: at(2023, time.January, 1), BatchSize: 10}
	summary, err := archive.Run(context.Background(), client, session, targets, opts, nil, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Moved != 25 {
		t.Errorf("moved %d messages, want 25", summary.Moved)
	}
	if summary.Failed != 0 {
		t.Errorf("failed %d messages, want 0", summary.Failed)
	}

	if got := srv.InMailbox(inboxID); len(got) != 1 || got[0] != "new-1" {
		t.Errorf("Inbox holds %v, want only [new-1]", got)
	}
	if got := len(srv.InMailbox(archiveID)); got != 26 {
		t.Errorf("Archive holds %d messages, want 26", got)
	}

	// Re-running the same command is a no-op — the filter no longer matches.
	second, err := archive.Run(context.Background(), client, session, targets, opts, nil, nil)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if second.Moved != 0 {
		t.Errorf("second run moved %d messages, want 0", second.Moved)
	}
}

// TestMovePreservesState pins the property the whole tool rests on: the patch
// touches mailboxIds and nothing else, and other mailboxes survive the move.
func TestMovePreservesState(t *testing.T) {
	srv := jmaptest.New(standardMailboxes(), []jmaptest.Message{{
		ID:         "m-1",
		MailboxIDs: map[string]bool{inboxID: true, labelID: true},
		Keywords:   map[string]bool{"$seen": true, "$flagged": true},
		ReceivedAt: at(2019, time.June, 1),
	}})
	defer srv.Close()
	client, session := connect(t, srv)
	targets := resolve(t, client, session)

	opts := archive.Options{Before: at(2023, time.January, 1), BatchSize: 10}
	if _, err := archive.Run(context.Background(), client, session, targets, opts, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	msg, ok := srv.Message("m-1")
	if !ok {
		t.Fatal("message disappeared — this tool must never delete anything")
	}
	if msg.MailboxIDs[inboxID] {
		t.Error("message is still in the Inbox")
	}
	if !msg.MailboxIDs[archiveID] {
		t.Error("message is not in the Archive")
	}
	if !msg.MailboxIDs[labelID] {
		t.Error("the Receipts label was dropped — the patch must not replace mailboxIds wholesale")
	}
	if !msg.Keywords["$seen"] || !msg.Keywords["$flagged"] {
		t.Error("keywords changed — read state and flags must survive a move")
	}
	if !msg.ReceivedAt.Equal(at(2019, time.June, 1)) {
		t.Error("receivedAt changed")
	}

	// Assert on the wire format, not just the outcome.
	if len(srv.SetRequests) != 1 {
		t.Fatalf("sent %d Email/set calls, want 1", len(srv.SetRequests))
	}
	var update map[string]map[string]json.RawMessage
	if err := json.Unmarshal(srv.SetRequests[0], &update); err != nil {
		t.Fatalf("parsing recorded update: %v", err)
	}
	for id, patch := range update {
		if len(patch) != 2 {
			t.Errorf("patch for %s has %d keys, want exactly 2", id, len(patch))
		}
		for key := range patch {
			if !strings.HasPrefix(key, "mailboxIds/") {
				t.Errorf("patch for %s touches %q; only mailboxIds/* is allowed", id, key)
			}
		}
		if got := string(patch["mailboxIds/"+inboxID]); got != "null" {
			t.Errorf("source removal is %s, want null", got)
		}
		if got := string(patch["mailboxIds/"+archiveID]); got != "true" {
			t.Errorf("destination add is %s, want true", got)
		}
	}
}

func TestKeepFlags(t *testing.T) {
	msgs := []jmaptest.Message{
		{ID: "read", MailboxIDs: map[string]bool{inboxID: true}, Keywords: map[string]bool{"$seen": true}, ReceivedAt: at(2019, time.June, 1)},
		{ID: "unread", MailboxIDs: map[string]bool{inboxID: true}, ReceivedAt: at(2019, time.June, 2)},
		{ID: "flagged", MailboxIDs: map[string]bool{inboxID: true}, Keywords: map[string]bool{"$seen": true, "$flagged": true}, ReceivedAt: at(2019, time.June, 3)},
	}

	tests := []struct {
		name      string
		opts      archive.Options
		wantMoved []string
	}{
		{"default moves everything old", archive.Options{}, []string{"flagged", "read", "unread"}},
		{"keep-unread", archive.Options{KeepUnread: true}, []string{"flagged", "read"}},
		{"keep-flagged", archive.Options{KeepFlagged: true}, []string{"read", "unread"}},
		{"both", archive.Options{KeepUnread: true, KeepFlagged: true}, []string{"read"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := jmaptest.New(standardMailboxes(), msgs)
			defer srv.Close()
			client, session := connect(t, srv)
			targets := resolve(t, client, session)

			opts := tc.opts
			opts.Before = at(2023, time.January, 1)
			opts.BatchSize = 10
			if _, err := archive.Run(context.Background(), client, session, targets, opts, nil, nil); err != nil {
				t.Fatalf("Run: %v", err)
			}

			got := srv.InMailbox(archiveID)
			if strings.Join(got, ",") != strings.Join(tc.wantMoved, ",") {
				t.Errorf("Archive holds %v, want %v", got, tc.wantMoved)
			}
		})
	}
}

// TestPerMessageFailureTerminates guards the loop's one real hazard: a message
// that always fails stays in the mailbox and would otherwise be re-queried
// forever.
func TestPerMessageFailureTerminates(t *testing.T) {
	srv := jmaptest.New(standardMailboxes(), []jmaptest.Message{
		{ID: "ok-1", MailboxIDs: map[string]bool{inboxID: true}, ReceivedAt: at(2019, time.June, 1)},
		{ID: "stuck", MailboxIDs: map[string]bool{inboxID: true}, ReceivedAt: at(2019, time.June, 2)},
	})
	srv.FailIDs["stuck"] = "message is locked"
	defer srv.Close()

	client, session := connect(t, srv)
	targets := resolve(t, client, session)
	opts := archive.Options{Before: at(2023, time.January, 1), BatchSize: 10}

	done := make(chan struct{})
	var summary archive.Summary
	var err error
	go func() {
		defer close(done)
		summary, err = archive.Run(context.Background(), client, session, targets, opts, nil, nil)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not terminate — a permanently failing message spun the drain loop")
	}

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Moved != 1 {
		t.Errorf("moved %d, want 1", summary.Moved)
	}
	if summary.Failed != 1 {
		t.Errorf("failed %d, want 1", summary.Failed)
	}
	if got := srv.InMailbox(inboxID); len(got) != 1 || got[0] != "stuck" {
		t.Errorf("Inbox holds %v, want only [stuck]", got)
	}
}

func TestCounts(t *testing.T) {
	srv := jmaptest.New(standardMailboxes(), []jmaptest.Message{
		{ID: "i-1", MailboxIDs: map[string]bool{inboxID: true}, ReceivedAt: at(2019, time.June, 1)},
		{ID: "i-2", MailboxIDs: map[string]bool{inboxID: true}, ReceivedAt: at(2019, time.June, 2)},
		{ID: "i-3", MailboxIDs: map[string]bool{inboxID: true}, ReceivedAt: at(2024, time.June, 2)},
		{ID: "s-1", MailboxIDs: map[string]bool{sentID: true}, ReceivedAt: at(2019, time.June, 2)},
	})
	defer srv.Close()

	client, session := connect(t, srv)
	targets := resolve(t, client, session, "Inbox", "Sent")
	opts := archive.Options{Before: at(2023, time.January, 1), BatchSize: 10}

	before := srv.RequestCount
	counts, err := archive.Counts(context.Background(), client, session, targets, opts)
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if got := srv.RequestCount - before; got != 1 {
		t.Errorf("counting took %d HTTP requests, want 1 — both mailboxes should batch into one", got)
	}
	if len(counts) != 2 || counts[0].Matched != 2 || counts[1].Matched != 1 {
		t.Fatalf("counts = %+v, want Inbox 2 and Sent 1", counts)
	}
	if archive.Total(counts) != 3 {
		t.Errorf("total = %d, want 3", archive.Total(counts))
	}
	// Counting must not move anything.
	if got := len(srv.InMailbox(archiveID)); got != 0 {
		t.Errorf("Archive holds %d messages after counting, want 0", got)
	}
}

func TestBatchClampedToServerLimit(t *testing.T) {
	var msgs []jmaptest.Message
	for i := range 12 {
		msgs = append(msgs, jmaptest.Message{
			ID:         fmt.Sprintf("m-%02d", i),
			MailboxIDs: map[string]bool{inboxID: true},
			ReceivedAt: at(2019, time.June, 1).Add(time.Duration(i) * time.Hour),
		})
	}
	srv := jmaptest.New(standardMailboxes(), msgs)
	// The fake rejects an Email/set larger than this, as Fastmail would.
	srv.MaxObjectsInSet = 5
	defer srv.Close()

	client, session := connect(t, srv)
	targets := resolve(t, client, session)
	opts := archive.Options{Before: at(2023, time.January, 1), BatchSize: 1000}

	summary, err := archive.Run(context.Background(), client, session, targets, opts, nil, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Moved != 12 {
		t.Errorf("moved %d, want 12", summary.Moved)
	}
	if len(srv.SetRequests) != 3 {
		t.Errorf("sent %d Email/set calls, want 3 batches of at most 5", len(srv.SetRequests))
	}
}

func TestRunHonorsContextCancellation(t *testing.T) {
	srv := jmaptest.New(standardMailboxes(), []jmaptest.Message{
		{ID: "m-1", MailboxIDs: map[string]bool{inboxID: true}, ReceivedAt: at(2019, time.June, 1)},
	})
	defer srv.Close()
	client, session := connect(t, srv)
	targets := resolve(t, client, session)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := archive.Run(ctx, client, session, targets,
		archive.Options{Before: at(2023, time.January, 1), BatchSize: 10}, nil, nil)
	if err == nil {
		t.Fatal("Run returned nil error for a cancelled context")
	}
	if len(srv.SetRequests) != 0 {
		t.Error("a cancelled run still moved messages")
	}
}
