// Package archive turns the user's arguments into JMAP queries and drains the
// matching messages into the Archive mailbox.
package archive

import (
	"fmt"
	"sort"
	"strings"

	"github.com/aaronsilber/email-archiver/internal/jmap"
)

// ProtectedRoles are mailboxes this tool refuses to read messages out of.
// Archive is the destination; Trash, Junk, and Drafts hold mail the user has
// already decided about, and sweeping them into Archive would be a surprise.
var ProtectedRoles = []string{
	jmap.RoleArchive,
	jmap.RoleTrash,
	jmap.RoleJunk,
	jmap.RoleDrafts,
}

func isProtected(role string) bool {
	for _, r := range ProtectedRoles {
		if role == r {
			return true
		}
	}
	return false
}

// Mailboxes indexes an account's mailboxes for lookup by role, name, and path.
type Mailboxes struct {
	All    []jmap.Mailbox
	byID   map[string]jmap.Mailbox
	byRole map[string]jmap.Mailbox
}

// NewMailboxes builds the index.
func NewMailboxes(list []jmap.Mailbox) *Mailboxes {
	m := &Mailboxes{
		All:    list,
		byID:   make(map[string]jmap.Mailbox, len(list)),
		byRole: make(map[string]jmap.Mailbox, len(list)),
	}
	for _, mb := range list {
		m.byID[mb.ID] = mb
		if mb.Role != "" {
			// First wins; a well-formed account has one mailbox per role.
			if _, seen := m.byRole[mb.Role]; !seen {
				m.byRole[mb.Role] = mb
			}
		}
	}
	return m
}

// Path renders a mailbox as a slash-separated path from the root, which is how
// ambiguous names are disambiguated in error messages.
func (m *Mailboxes) Path(mb jmap.Mailbox) string {
	parts := []string{mb.Name}
	seen := map[string]bool{mb.ID: true}
	for parent := mb.ParentID; parent != ""; {
		p, ok := m.byID[parent]
		if !ok || seen[p.ID] {
			break
		}
		seen[p.ID] = true
		parts = append(parts, p.Name)
		parent = p.ParentID
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, "/")
}

// Role returns the mailbox holding the given role.
func (m *Mailboxes) Role(role string) (jmap.Mailbox, bool) {
	mb, ok := m.byRole[role]
	return mb, ok
}

// Lookup resolves a user-supplied mailbox name. It matches, in order: a JMAP
// role name, a full slash-separated path, then a bare mailbox name — each
// case-insensitively. A bare name matching more than one mailbox is an error
// listing the candidate paths, since guessing could archive the wrong mail.
func (m *Mailboxes) Lookup(name string) (jmap.Mailbox, error) {
	want := strings.ToLower(strings.TrimSpace(strings.Trim(name, "/")))
	if want == "" {
		return jmap.Mailbox{}, fmt.Errorf("empty mailbox name")
	}

	if mb, ok := m.byRole[want]; ok {
		return mb, nil
	}

	var pathMatches, nameMatches []jmap.Mailbox
	for _, mb := range m.All {
		if strings.ToLower(m.Path(mb)) == want {
			pathMatches = append(pathMatches, mb)
		}
		if strings.ToLower(mb.Name) == want {
			nameMatches = append(nameMatches, mb)
		}
	}

	for _, candidates := range [][]jmap.Mailbox{pathMatches, nameMatches} {
		switch len(candidates) {
		case 0:
			continue
		case 1:
			return candidates[0], nil
		default:
			paths := make([]string, 0, len(candidates))
			for _, mb := range candidates {
				paths = append(paths, m.Path(mb))
			}
			sort.Strings(paths)
			return jmap.Mailbox{}, fmt.Errorf("mailbox %q is ambiguous — matches %s; pass the full path",
				name, strings.Join(paths, ", "))
		}
	}
	return jmap.Mailbox{}, fmt.Errorf("no mailbox named %q in this account", name)
}

// Targets is the validated source/destination pair for a run.
type Targets struct {
	Sources []jmap.Mailbox
	Archive jmap.Mailbox
}

// Resolve validates the destination and the requested sources. It refuses any
// source in ProtectedRoles, and does so before a single message is touched.
// An empty names slice defaults to the Inbox.
func Resolve(m *Mailboxes, names []string) (Targets, error) {
	dst, ok := m.Role(jmap.RoleArchive)
	if !ok {
		return Targets{}, fmt.Errorf("this account has no Archive mailbox (no mailbox with role %q) — create one in Fastmail first", jmap.RoleArchive)
	}

	if len(names) == 0 {
		inbox, ok := m.Role(jmap.RoleInbox)
		if !ok {
			return Targets{}, fmt.Errorf("this account has no Inbox (no mailbox with role %q) — pass --from explicitly", jmap.RoleInbox)
		}
		return Targets{Sources: []jmap.Mailbox{inbox}, Archive: dst}, nil
	}

	var sources []jmap.Mailbox
	seen := map[string]bool{}
	for _, name := range names {
		mb, err := m.Lookup(name)
		if err != nil {
			return Targets{}, err
		}
		if isProtected(mb.Role) {
			return Targets{}, fmt.Errorf("refusing to archive out of %s: it is the %s mailbox", m.Path(mb), mb.Role)
		}
		if seen[mb.ID] {
			continue
		}
		seen[mb.ID] = true
		sources = append(sources, mb)
	}
	return Targets{Sources: sources, Archive: dst}, nil
}
