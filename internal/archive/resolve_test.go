package archive_test

import (
	"strings"
	"testing"

	"github.com/aaronsilber/email-archiver/internal/archive"
	"github.com/aaronsilber/email-archiver/internal/jmap"
)

func mailboxes() *archive.Mailboxes {
	return archive.NewMailboxes([]jmap.Mailbox{
		{ID: inboxID, Name: "Inbox", Role: jmap.RoleInbox},
		{ID: archiveID, Name: "Archive", Role: jmap.RoleArchive},
		{ID: trashID, Name: "Trash", Role: jmap.RoleTrash},
		{ID: "mb-junk", Name: "Spam", Role: jmap.RoleJunk},
		{ID: "mb-drafts", Name: "Drafts", Role: jmap.RoleDrafts},
		{ID: sentID, Name: "Sent", Role: jmap.RoleSent},
		{ID: "mb-parent", Name: "Clients"},
		{ID: "mb-a", Name: "Receipts", ParentID: "mb-parent"},
		{ID: "mb-b", Name: "Receipts", ParentID: "mb-work"},
		{ID: "mb-work", Name: "Work"},
		// A root mailbox whose bare name also collides with a nested one:
		// the root's full path is "Notes", so it wins outright.
		{ID: "mb-notes", Name: "Notes"},
		{ID: "mb-notes-nested", Name: "Notes", ParentID: "mb-work"},
	})
}

// TestResolveRefusesProtectedMailboxes is the safety net: the tool must never
// sweep Trash, Junk, Drafts, or Archive itself into Archive.
func TestResolveRefusesProtectedMailboxes(t *testing.T) {
	for _, name := range []string{"Trash", "Spam", "Drafts", "Archive", "trash", "junk"} {
		t.Run(name, func(t *testing.T) {
			_, err := archive.Resolve(mailboxes(), []string{name})
			if err == nil {
				t.Fatalf("Resolve(%q) succeeded; it must refuse protected mailboxes", name)
			}
			if !strings.Contains(err.Error(), "refusing") {
				t.Errorf("error %q does not explain the refusal", err)
			}
		})
	}
}

func TestResolveDefaultsToInbox(t *testing.T) {
	targets, err := archive.Resolve(mailboxes(), nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(targets.Sources) != 1 || targets.Sources[0].ID != inboxID {
		t.Errorf("sources = %+v, want just the Inbox", targets.Sources)
	}
	if targets.Archive.ID != archiveID {
		t.Errorf("destination = %s, want %s", targets.Archive.ID, archiveID)
	}
}

func TestResolveByRoleNameAndPath(t *testing.T) {
	tests := []struct{ input, wantID string }{
		{"Inbox", inboxID},
		{"inbox", inboxID},
		{"SENT", sentID},
		{"Clients/Receipts", "mb-a"},
		{"clients/receipts", "mb-a"},
		{"Work/Receipts", "mb-b"},
		// An exact path match beats a bare-name collision.
		{"Notes", "mb-notes"},
		{"Work/Notes", "mb-notes-nested"},
	}
	for _, tc := range tests {
		targets, err := archive.Resolve(mailboxes(), []string{tc.input})
		if err != nil {
			t.Errorf("Resolve(%q): %v", tc.input, err)
			continue
		}
		if targets.Sources[0].ID != tc.wantID {
			t.Errorf("Resolve(%q) = %s, want %s", tc.input, targets.Sources[0].ID, tc.wantID)
		}
	}
}

func TestResolveRejectsAmbiguousName(t *testing.T) {
	_, err := archive.Resolve(mailboxes(), []string{"Receipts"})
	if err == nil {
		t.Fatal("Resolve succeeded on an ambiguous name; it must not guess")
	}
	if !strings.Contains(err.Error(), "Clients/Receipts") || !strings.Contains(err.Error(), "Work/Receipts") {
		t.Errorf("error %q should list both candidate paths", err)
	}
}

func TestResolveRejectsUnknownName(t *testing.T) {
	if _, err := archive.Resolve(mailboxes(), []string{"Nope"}); err == nil {
		t.Fatal("Resolve succeeded on an unknown mailbox")
	}
}

func TestResolveDeduplicatesSources(t *testing.T) {
	targets, err := archive.Resolve(mailboxes(), []string{"Inbox", "inbox", "Inbox"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(targets.Sources) != 1 {
		t.Errorf("sources = %d, want 1 after deduplication", len(targets.Sources))
	}
}

func TestResolveRequiresArchiveMailbox(t *testing.T) {
	m := archive.NewMailboxes([]jmap.Mailbox{{ID: inboxID, Name: "Inbox", Role: jmap.RoleInbox}})
	_, err := archive.Resolve(m, nil)
	if err == nil {
		t.Fatal("Resolve succeeded without an Archive mailbox")
	}
	if !strings.Contains(err.Error(), "Archive") {
		t.Errorf("error %q should name the missing mailbox", err)
	}
}
