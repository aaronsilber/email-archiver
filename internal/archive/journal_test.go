package archive_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aaronsilber/email-archiver/internal/archive"
	"github.com/aaronsilber/email-archiver/internal/jmaptest"
)

func TestJournalKeyIsStableAcrossArgumentOrder(t *testing.T) {
	opts := archive.Options{Before: at(2023, time.January, 1)}
	a := archive.JournalKey("acct-1", opts, []string{"mb-inbox", "mb-sent"})
	b := archive.JournalKey("acct-1", opts, []string{"mb-sent", "mb-inbox"})
	if a != b {
		t.Error("journal key changed with source order; a resumed run would lose its history")
	}

	other := archive.JournalKey("acct-1", archive.Options{Before: at(2022, time.January, 1)}, []string{"mb-inbox"})
	if a == other {
		t.Error("different --before values produced the same journal key")
	}
	if archive.JournalKey("acct-2", opts, []string{"mb-inbox"}) == a {
		t.Error("different accounts produced the same journal key")
	}
	if archive.JournalKey("acct-1", archive.Options{Before: opts.Before, KeepUnread: true}, []string{"mb-inbox"}) == a {
		t.Error("--keep-unread did not change the journal key")
	}
}

func TestJournalRoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	j, err := archive.OpenJournal(dir, "abc123", []string{"--before", "2023-01-01"}, now)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	if j.PriorMoved() != 0 {
		t.Errorf("a fresh journal reports %d prior moves, want 0", j.PriorMoved())
	}

	j.RecordBatch("Inbox", 500)
	j.RecordBatch("Inbox", 250)
	j.Finish(now)
	if err := j.SaveErr(); err != nil {
		t.Fatalf("saving: %v", err)
	}

	// A later run of the same command finds the same journal.
	reopened, err := archive.OpenJournal(dir, "abc123", []string{"--before", "2023-01-01"}, now)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	if got := reopened.PriorMoved(); got != 750 {
		t.Errorf("prior moved = %d, want 750", got)
	}
	if reopened.Batches != 2 {
		t.Errorf("batches = %d, want 2", reopened.Batches)
	}

	info, err := os.Stat(filepath.Join(dir, "run-abc123.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("journal mode = %04o, want 0600", perm)
	}
}

// TestJournalIgnoresCorruptFile keeps a damaged advisory file from blocking a
// run — the drain loop, not the journal, is what makes a resume correct.
func TestJournalIgnoresCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run-bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	j, err := archive.OpenJournal(dir, "bad", nil, time.Now())
	if err == nil {
		t.Error("OpenJournal silently ignored a corrupt journal; it should say so")
	}
	if j == nil {
		t.Fatal("OpenJournal returned no journal for a corrupt file; the run must continue")
	}
	if j.PriorMoved() != 0 {
		t.Errorf("prior moved = %d, want 0", j.PriorMoved())
	}
}

// TestJournalRecordsDuringRun wires the journal into a real run and checks the
// counts it ends up holding.
func TestJournalRecordsDuringRun(t *testing.T) {
	var msgs []jmaptest.Message
	for i := range 7 {
		msgs = append(msgs, jmaptest.Message{
			ID:         fmt.Sprintf("m-%02d", i),
			MailboxIDs: map[string]bool{inboxID: true},
			ReceivedAt: at(2019, time.June, 1).Add(time.Duration(i) * time.Hour),
		})
	}
	srv := jmaptest.New(standardMailboxes(), msgs)
	defer srv.Close()
	client, session := connect(t, srv)
	targets := resolve(t, client, session)

	dir := t.TempDir()
	j, err := archive.OpenJournal(dir, "run-key", []string{"--before", "2023-01-01"}, time.Now())
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}

	opts := archive.Options{Before: at(2023, time.January, 1), BatchSize: 3}
	if _, err := archive.Run(context.Background(), client, session, targets, opts, j, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	j.Finish(time.Now())
	if err := j.SaveErr(); err != nil {
		t.Fatalf("journal write failed: %v", err)
	}

	reopened, err := archive.OpenJournal(dir, "run-key", nil, time.Now())
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	if got := reopened.PriorMoved(); got != 7 {
		t.Errorf("journal recorded %d moves, want 7", got)
	}
	if reopened.Batches != 3 {
		t.Errorf("journal recorded %d batches, want 3", reopened.Batches)
	}
}
