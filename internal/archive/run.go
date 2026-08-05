package archive

import (
	"context"
	"fmt"

	"github.com/aaronsilber/email-archiver/internal/jmap"
)

// BatchEvent reports one completed batch to the caller for progress output.
type BatchEvent struct {
	Mailbox    jmap.Mailbox
	Moved      int // messages moved in this batch
	MovedTotal int // messages moved in this mailbox so far
	Remaining  int // still matching, as of the query that opened this batch
}

// MailboxResult is the outcome for one source mailbox.
type MailboxResult struct {
	Mailbox jmap.Mailbox
	Moved   int
	Failed  map[string]jmap.SetError
}

// Summary is the outcome of a whole run.
type Summary struct {
	Results []MailboxResult
	Moved   int
	Failed  int
}

// Run drains every source mailbox into the Archive mailbox.
//
// The loop always queries at position 0 rather than paging. A moved message
// leaves the source mailbox and so stops matching the filter, which means the
// result set shrinks with every batch until it is empty. Three properties fall
// out of that single fact: the run is idempotent (a second run matches
// nothing), it is resumable (an interrupted run resumes by re-running the same
// command), and it cannot skip messages the way offset paging does when the
// underlying set mutates mid-run.
func Run(ctx context.Context, c *jmap.Client, s *jmap.Session, t Targets, o Options, j *Journal, onBatch func(BatchEvent)) (Summary, error) {
	batch := clampBatch(o.BatchSize, s.Limits)

	summary := Summary{Results: make([]MailboxResult, 0, len(t.Sources))}
	for _, src := range t.Sources {
		result := MailboxResult{Mailbox: src, Failed: map[string]jmap.SetError{}}
		filter := o.Filter(src.ID)

		for {
			if err := ctx.Err(); err != nil {
				summary.append(result)
				return summary, err
			}

			q, err := c.QueryEmails(ctx, s, filter, batch, true)
			if err != nil {
				summary.append(result)
				return summary, fmt.Errorf("querying %s: %w", src.Name, err)
			}

			// Messages that already failed to move stay in the mailbox and so
			// come back on every subsequent query. Skipping them is what keeps
			// a permanent per-message failure from spinning forever.
			ids := make([]string, 0, len(q.IDs))
			for _, id := range q.IDs {
				if _, failed := result.Failed[id]; !failed {
					ids = append(ids, id)
				}
			}
			if len(ids) == 0 {
				break
			}

			moved, err := c.MoveEmails(ctx, s, ids, src.ID, t.Archive.ID)
			if err != nil {
				summary.append(result)
				return summary, fmt.Errorf("moving from %s: %w", src.Name, err)
			}
			for id, setErr := range moved.NotUpdated {
				result.Failed[id] = setErr
			}
			result.Moved += len(moved.Updated)

			if j != nil {
				// The journal is advisory — a failure to write it is recorded
				// on the journal and surfaced once at the end, never fatal.
				j.RecordBatch(src.Name, len(moved.Updated))
				j.SaveBestEffort()
			}
			if onBatch != nil {
				onBatch(BatchEvent{
					Mailbox:    src,
					Moved:      len(moved.Updated),
					MovedTotal: result.Moved,
					Remaining:  q.Total,
				})
			}

			if len(moved.Updated) == 0 {
				// Every message in this batch failed. Retrying yields the same
				// batch, so stop on this mailbox and report it.
				break
			}
		}

		summary.append(result)
	}
	return summary, nil
}

func (s *Summary) append(r MailboxResult) {
	s.Results = append(s.Results, r)
	s.Moved += r.Moved
	s.Failed += len(r.Failed)
}

// clampBatch keeps the batch inside both the server's set and get limits.
func clampBatch(requested int, l jmap.Limits) int {
	if requested < 1 {
		requested = 1
	}
	if l.MaxObjectsInSet > 0 {
		requested = min(requested, l.MaxObjectsInSet)
	}
	if l.MaxObjectsInGet > 0 {
		requested = min(requested, l.MaxObjectsInGet)
	}
	return requested
}
