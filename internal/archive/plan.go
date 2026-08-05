package archive

import (
	"context"
	"fmt"
	"time"

	"github.com/aaronsilber/email-archiver/internal/jmap"
)

// JMAP keyword names for read and flagged state.
const (
	keywordSeen    = "$seen"
	keywordFlagged = "$flagged"
)

// Options are the run parameters derived from the command line.
type Options struct {
	// Before is exclusive: messages with receivedAt strictly earlier match.
	Before time.Time
	// KeepUnread restricts the run to messages already marked read.
	KeepUnread bool
	// KeepFlagged excludes flagged messages.
	KeepFlagged bool
	// BatchSize is the number of messages moved per round trip.
	BatchSize int
}

// Filter builds the Email/query filter for one source mailbox.
func (o Options) Filter(srcMailboxID string) any {
	before := jmap.UTCDate(o.Before)
	conditions := []any{
		jmap.FilterCondition{InMailbox: srcMailboxID},
		jmap.FilterCondition{Before: &before},
	}
	if o.KeepUnread {
		// Only already-read mail moves; unread mail stays put.
		conditions = append(conditions, jmap.FilterCondition{HasKeyword: keywordSeen})
	}
	if o.KeepFlagged {
		conditions = append(conditions, jmap.FilterCondition{NotKeyword: keywordFlagged})
	}
	return jmap.FilterOperator{Operator: "AND", Conditions: conditions}
}

// Count is how many messages match in one mailbox.
type Count struct {
	Mailbox jmap.Mailbox
	Matched int
}

// Counts reports what would move, per source mailbox. All mailboxes are asked
// in a single HTTP request (split only if the server's maxCallsInRequest is
// smaller than the number of sources).
func Counts(ctx context.Context, c *jmap.Client, s *jmap.Session, t Targets, o Options) ([]Count, error) {
	perRequest := s.Limits.MaxCallsInRequest
	if perRequest < 1 {
		perRequest = 1
	}

	out := make([]Count, 0, len(t.Sources))
	for start := 0; start < len(t.Sources); start += perRequest {
		end := min(start+perRequest, len(t.Sources))
		chunk := t.Sources[start:end]

		filters := make([]any, len(chunk))
		for i, mb := range chunk {
			filters[i] = o.Filter(mb.ID)
		}
		// limit 0: we want the total, not the ids.
		results, err := c.QueryEmailsBatch(ctx, s, filters, 0, true)
		if err != nil {
			return nil, err
		}
		for i, r := range results {
			if !r.TotalKnown {
				return nil, fmt.Errorf("server did not return a total for mailbox %q", chunk[i].Name)
			}
			out = append(out, Count{Mailbox: chunk[i], Matched: r.Total})
		}
	}
	return out, nil
}

// Total sums the per-mailbox counts.
func Total(counts []Count) int {
	total := 0
	for _, c := range counts {
		total += c.Matched
	}
	return total
}
