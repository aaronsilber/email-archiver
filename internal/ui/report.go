// Package ui renders the count table, the confirmation prompt, and progress
// output. Everything user-facing goes through here so the CLI stays wiring.
package ui

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/aaronsilber/email-archiver/internal/archive"
	"github.com/aaronsilber/email-archiver/internal/jmap"
)

// Printer writes user-facing output.
type Printer struct {
	Out io.Writer
	Err io.Writer
}

// New builds a Printer.
func New(out, errOut io.Writer) *Printer {
	return &Printer{Out: out, Err: errOut}
}

func (p *Printer) printf(format string, args ...any) {
	fmt.Fprintf(p.Out, format, args...)
}

// Warnf writes a warning to stderr.
func (p *Printer) Warnf(format string, args ...any) {
	fmt.Fprintf(p.Err, "warning: "+format+"\n", args...)
}

// Counts prints the per-mailbox table of what matched. This is the "show the
// damage first" step: nothing moves until the user has seen these numbers.
func (p *Printer) Counts(counts []archive.Count, dest jmap.Mailbox, before string) {
	p.printf("Messages received before %s\n\n", before)

	nameWidth := len("MAILBOX")
	countWidth := len("MATCHING")
	for _, c := range counts {
		nameWidth = max(nameWidth, len(c.Mailbox.Name))
		countWidth = max(countWidth, len(humanCount(c.Matched)))
	}

	p.printf("  %-*s  %*s\n", nameWidth, "MAILBOX", countWidth, "MATCHING")
	for _, c := range counts {
		p.printf("  %-*s  %*s\n", nameWidth, c.Mailbox.Name, countWidth, humanCount(c.Matched))
	}

	total := archive.Total(counts)
	if len(counts) > 1 {
		p.printf("  %-*s  %*s\n", nameWidth, strings.Repeat("─", nameWidth), countWidth, strings.Repeat("─", countWidth))
		p.printf("  %-*s  %*s\n", nameWidth, "total", countWidth, humanCount(total))
	}

	p.printf("\nDestination: %s\n", dest.Name)
}

// DryRun prints the closing line of a --dry-run.
func (p *Printer) DryRun(total int) {
	if total == 0 {
		p.printf("\nNothing to do.\n")
		return
	}
	p.printf("\nDry run: nothing moved. Re-run without --dry-run to archive %s.\n", pluralMessages(total))
}

// Resume mentions work recorded by an earlier run of the same command.
func (p *Printer) Resume(prior int, path string) {
	p.printf("Resuming: an earlier run of this command moved %s (%s).\n\n", pluralMessages(prior), path)
}

// Confirm asks for approval and reports whether the run may proceed. Anything
// other than y/yes is a no.
func (p *Printer) Confirm(in io.Reader, total int, dest string) (bool, error) {
	p.printf("\nMove %s to %s? [y/N] ", pluralMessages(total), dest)

	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		if err == io.EOF {
			p.printf("\n")
			return false, nil
		}
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// Batch prints one progress line per completed batch.
func (p *Printer) Batch(e archive.BatchEvent) {
	remaining := max(e.Remaining-e.Moved, 0)
	p.printf("  %s: moved %s (%s remaining)\n",
		e.Mailbox.Name, humanCount(e.MovedTotal), humanCount(remaining))
}

// Summary prints the closing report and any per-message failures.
func (p *Printer) Summary(s archive.Summary, dest jmap.Mailbox) {
	p.printf("\nMoved %s to %s.\n", pluralMessages(s.Moved), dest.Name)

	if s.Failed == 0 {
		return
	}
	fmt.Fprintf(p.Err, "\n%s could not be moved:\n", pluralMessages(s.Failed))
	for _, r := range s.Results {
		for id, setErr := range r.Failed {
			detail := setErr.Description
			if detail == "" {
				detail = setErr.Type
			}
			fmt.Fprintf(p.Err, "  %s %s: %s\n", r.Mailbox.Name, id, detail)
		}
	}
	fmt.Fprintf(p.Err, "\nEverything else moved. Re-run the same command to retry these.\n")
}

func pluralMessages(n int) string {
	if n == 1 {
		return "1 message"
	}
	return humanCount(n) + " messages"
}

// humanCount groups thousands, since these numbers run to six figures.
func humanCount(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")

	var b strings.Builder
	for i, digit := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(digit)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
