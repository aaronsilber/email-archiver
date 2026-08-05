package ui_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aaronsilber/email-archiver/internal/archive"
	"github.com/aaronsilber/email-archiver/internal/jmap"
	"github.com/aaronsilber/email-archiver/internal/ui"
)

func TestCountsTable(t *testing.T) {
	var out, errOut bytes.Buffer
	p := ui.New(&out, &errOut)

	p.Counts([]archive.Count{
		{Mailbox: jmap.Mailbox{Name: "Inbox"}, Matched: 104233},
		{Mailbox: jmap.Mailbox{Name: "Sent"}, Matched: 12},
	}, jmap.Mailbox{Name: "Archive"}, "2023-01-01 00:00 EST")

	got := out.String()
	for _, want := range []string{"104,233", "Inbox", "Sent", "total", "Archive", "2023-01-01"} {
		if !strings.Contains(got, want) {
			t.Errorf("count table missing %q:\n%s", want, got)
		}
	}
}

func TestConfirm(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{"n\n", false},
		{"\n", false},
		{"", false}, // EOF, e.g. a closed stdin
		{"maybe\n", false},
	}

	for _, tc := range tests {
		var out, errOut bytes.Buffer
		p := ui.New(&out, &errOut)
		got, err := p.Confirm(strings.NewReader(tc.input), 10, "Archive")
		if err != nil {
			t.Fatalf("Confirm(%q): %v", tc.input, err)
		}
		if got != tc.want {
			t.Errorf("Confirm(%q) = %v, want %v", tc.input, got, tc.want)
		}
		if !strings.Contains(out.String(), "[y/N]") {
			t.Errorf("prompt does not show the default: %q", out.String())
		}
	}
}

func TestSummaryReportsFailures(t *testing.T) {
	var out, errOut bytes.Buffer
	p := ui.New(&out, &errOut)

	p.Summary(archive.Summary{
		Moved:  3,
		Failed: 1,
		Results: []archive.MailboxResult{{
			Mailbox: jmap.Mailbox{Name: "Inbox"},
			Moved:   3,
			Failed:  map[string]jmap.SetError{"m-9": {Type: "forbidden", Description: "message is locked"}},
		}},
	}, jmap.Mailbox{Name: "Archive"})

	if !strings.Contains(out.String(), "Moved 3 messages to Archive") {
		t.Errorf("summary = %q", out.String())
	}
	for _, want := range []string{"m-9", "message is locked", "Re-run"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("failure report missing %q:\n%s", want, errOut.String())
		}
	}
}

func TestBatchProgress(t *testing.T) {
	var out, errOut bytes.Buffer
	p := ui.New(&out, &errOut)

	p.Batch(archive.BatchEvent{
		Mailbox:    jmap.Mailbox{Name: "Inbox"},
		Moved:      500,
		MovedTotal: 1500,
		Remaining:  4000,
	})

	got := out.String()
	// Remaining is reported after this batch's moves, not before.
	if !strings.Contains(got, "1,500") || !strings.Contains(got, "3,500") {
		t.Errorf("progress line = %q, want moved 1,500 and 3,500 remaining", got)
	}
}

func TestSingularMessage(t *testing.T) {
	var out, errOut bytes.Buffer
	p := ui.New(&out, &errOut)
	p.DryRun(1)
	if !strings.Contains(out.String(), "1 message.") {
		t.Errorf("dry-run line = %q, want a singular message", out.String())
	}
}
