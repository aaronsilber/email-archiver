package main

import (
	"context"
	"testing"
	"time"

	"github.com/aaronsilber/email-archiver/internal/config"
	"github.com/aaronsilber/email-archiver/internal/jmap"
	"github.com/aaronsilber/email-archiver/internal/jmaptest"
)

// startFake stands up a fake Fastmail account and points the process at it:
// token in the environment, state and config in temp dirs.
func startFake(t *testing.T, messages []jmaptest.Message) *jmaptest.Server {
	t.Helper()
	srv := jmaptest.New([]jmaptest.Mailbox{
		{ID: "mb-inbox", Name: "Inbox", Role: jmap.RoleInbox},
		{ID: "mb-archive", Name: "Archive", Role: jmap.RoleArchive},
		{ID: "mb-trash", Name: "Trash", Role: jmap.RoleTrash},
	}, messages)
	t.Cleanup(srv.Close)

	t.Setenv(config.EnvVar, "test-token")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return srv
}

func oldMessages(n int) []jmaptest.Message {
	msgs := make([]jmaptest.Message, 0, n)
	for i := range n {
		msgs = append(msgs, jmaptest.Message{
			ID:         string(rune('a'+i%26)) + time.Duration(i).String(),
			MailboxIDs: map[string]bool{"mb-inbox": true},
			ReceivedAt: time.Date(2019, 6, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour),
		})
	}
	return msgs
}

func TestEndToEndDryRunChangesNothing(t *testing.T) {
	srv := startFake(t, oldMessages(5))

	opts := &options{
		before:     "2023-01-01",
		dryRun:     true,
		batch:      500,
		sessionURL: srv.SessionURL(),
	}
	code, err := archiveRun(context.Background(), opts)
	if err != nil {
		t.Fatalf("archiveRun: %v", err)
	}
	if code != exitOK {
		t.Errorf("exit code = %d, want %d", code, exitOK)
	}
	if got := len(srv.SetRequests); got != 0 {
		t.Errorf("a dry run made %d Email/set calls, want 0", got)
	}
	if got := len(srv.InMailbox("mb-inbox")); got != 5 {
		t.Errorf("Inbox holds %d messages after a dry run, want 5", got)
	}
}

func TestEndToEndArchives(t *testing.T) {
	srv := startFake(t, oldMessages(5))

	opts := &options{
		before:     "2023-01-01",
		yes:        true,
		batch:      2,
		sessionURL: srv.SessionURL(),
	}
	code, err := archiveRun(context.Background(), opts)
	if err != nil {
		t.Fatalf("archiveRun: %v", err)
	}
	if code != exitOK {
		t.Errorf("exit code = %d, want %d", code, exitOK)
	}
	if got := len(srv.InMailbox("mb-inbox")); got != 0 {
		t.Errorf("Inbox holds %d messages, want 0", got)
	}
	if got := len(srv.InMailbox("mb-archive")); got != 5 {
		t.Errorf("Archive holds %d messages, want 5", got)
	}

	// The second run is the idempotence check, end to end this time.
	before := len(srv.SetRequests)
	if _, err := archiveRun(context.Background(), opts); err != nil {
		t.Fatalf("second archiveRun: %v", err)
	}
	if len(srv.SetRequests) != before {
		t.Error("a repeat run issued another Email/set; it should have found nothing to move")
	}
}

func TestEndToEndRefusesProtectedSource(t *testing.T) {
	srv := startFake(t, oldMessages(1))

	opts := &options{
		before:     "2023-01-01",
		from:       stringList{"Trash"},
		yes:        true,
		batch:      500,
		sessionURL: srv.SessionURL(),
	}
	code, err := archiveRun(context.Background(), opts)
	if err == nil {
		t.Fatal("archiveRun accepted Trash as a source")
	}
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if len(srv.SetRequests) != 0 {
		t.Error("the refusal came too late — messages were already moved")
	}
}

func TestEndToEndBadTokenExitsUsage(t *testing.T) {
	srv := startFake(t, oldMessages(1))
	srv.Unauthorized = true

	code, err := archiveRun(context.Background(), &options{
		before:     "2023-01-01",
		yes:        true,
		batch:      500,
		sessionURL: srv.SessionURL(),
	})
	if err == nil {
		t.Fatal("archiveRun succeeded against a 401")
	}
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
}

func TestEndToEndPartialFailureExitsOne(t *testing.T) {
	msgs := oldMessages(3)
	srv := startFake(t, msgs)
	srv.FailIDs[msgs[1].ID] = "message is locked"

	code, err := archiveRun(context.Background(), &options{
		before:     "2023-01-01",
		yes:        true,
		batch:      500,
		sessionURL: srv.SessionURL(),
	})
	if err != nil {
		t.Fatalf("archiveRun: %v", err)
	}
	if code != exitPartial {
		t.Errorf("exit code = %d, want %d for a partial failure", code, exitPartial)
	}
	if got := len(srv.InMailbox("mb-archive")); got != 2 {
		t.Errorf("Archive holds %d messages, want the 2 that succeeded", got)
	}
}
