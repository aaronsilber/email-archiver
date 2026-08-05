// Command email-archiver moves Fastmail messages older than a given date out
// of the Inbox and into the Archive mailbox. It never deletes anything.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aaronsilber/email-archiver/internal/archive"
	"github.com/aaronsilber/email-archiver/internal/config"
	"github.com/aaronsilber/email-archiver/internal/jmap"
	"github.com/aaronsilber/email-archiver/internal/ui"
)

const version = "0.1.0"

// Exit codes.
const (
	exitOK      = 0
	exitPartial = 1 // some messages could not be moved, or the run was interrupted
	exitUsage   = 2 // bad arguments, missing credentials, unsafe request
)

// stringList collects a repeatable flag such as --from.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	if strings.TrimSpace(v) == "" {
		return errors.New("mailbox name cannot be empty")
	}
	*s = append(*s, v)
	return nil
}

type options struct {
	before      string
	from        stringList
	dryRun      bool
	yes         bool
	keepUnread  bool
	keepFlagged bool
	batch       int
	verbose     bool
	showVersion bool
	sessionURL  string
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	opts, code, err := parseFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return code
	}
	if opts == nil {
		return exitOK // --help or --version already printed
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	code, err = archiveRun(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	return code
}

func parseFlags(args []string) (*options, int, error) {
	var o options
	fs := flag.NewFlagSet("email-archiver", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { usage(fs) }

	fs.StringVar(&o.before, "before", "", "archive messages received strictly before this date (YYYY-MM-DD or RFC3339) — required")
	fs.Var(&o.from, "from", "source mailbox; repeatable (default: Inbox)")
	fs.BoolVar(&o.dryRun, "dry-run", false, "report what would move, change nothing")
	fs.BoolVar(&o.yes, "yes", false, "skip the confirmation prompt")
	fs.BoolVar(&o.keepUnread, "keep-unread", false, "leave unread messages where they are")
	fs.BoolVar(&o.keepFlagged, "keep-flagged", false, "leave flagged messages where they are")
	fs.IntVar(&o.batch, "batch", 500, "messages moved per request")
	fs.BoolVar(&o.verbose, "verbose", false, "log each HTTP request")
	fs.BoolVar(&o.showVersion, "version", false, "print the version and exit")
	fs.StringVar(&o.sessionURL, "session-url", jmap.DefaultSessionURL, "JMAP session endpoint")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, exitOK, nil
		}
		return nil, exitUsage, err
	}
	if o.showVersion {
		fmt.Printf("email-archiver %s\n", version)
		return nil, exitOK, nil
	}
	if fs.NArg() > 0 {
		return nil, exitUsage, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if o.before == "" {
		usage(fs)
		return nil, exitUsage, errors.New("--before is required")
	}
	if o.batch < 1 {
		return nil, exitUsage, fmt.Errorf("--batch must be at least 1")
	}
	return &o, exitOK, nil
}

func usage(fs *flag.FlagSet) {
	fmt.Fprint(os.Stderr, `email-archiver — move old Fastmail messages into Archive.

Nothing is ever deleted. Messages are moved exactly as clicking Archive in the
Fastmail web UI would move them: read state, flags, and received dates are all
preserved.

Usage:
  email-archiver --before 2023-01-01 --dry-run
  email-archiver --before 2023-01-01
  email-archiver --before 2023-01-01 --from Inbox --from Sent

Credentials come from $FASTMAIL_API_TOKEN or the config file, never from a
flag. Options:
`)
	fs.PrintDefaults()
}

func archiveRun(ctx context.Context, o *options) (int, error) {
	before, err := parseBefore(o.before)
	if err != nil {
		return exitUsage, err
	}

	cfg, err := config.Load()
	if err != nil {
		return exitUsage, err
	}

	out := ui.New(os.Stdout, os.Stderr)
	client := jmap.NewClient(o.sessionURL, cfg.Token)
	if o.verbose {
		client.Trace = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "jmap: "+format+"\n", args...)
		}
		fmt.Fprintf(os.Stderr, "jmap: token from %s\n", cfg.Source)
	}

	session, err := client.FetchSession(ctx)
	if err != nil {
		return exitUsage, err
	}

	list, err := client.GetMailboxes(ctx, session)
	if err != nil {
		return exitPartial, err
	}
	mailboxes := archive.NewMailboxes(list)
	targets, err := archive.Resolve(mailboxes, o.from)
	if err != nil {
		return exitUsage, err
	}

	runOpts := archive.Options{
		Before:      before,
		KeepUnread:  o.keepUnread,
		KeepFlagged: o.keepFlagged,
		BatchSize:   o.batch,
	}

	journal, journalErr := openJournal(session, runOpts, targets)
	if journalErr != nil {
		out.Warnf("%v", journalErr)
	}
	if journal != nil && journal.PriorMoved() > 0 {
		out.Resume(journal.PriorMoved(), journal.Path())
	}

	counts, err := archive.Counts(ctx, client, session, targets, runOpts)
	if err != nil {
		return exitPartial, err
	}
	out.Counts(counts, targets.Archive, before.Format("2006-01-02 15:04 MST"))

	total := archive.Total(counts)
	if o.dryRun {
		out.DryRun(total)
		return exitOK, nil
	}
	if total == 0 {
		out.DryRun(0)
		return exitOK, nil
	}

	if !o.yes {
		if !stdinIsTerminal() {
			return exitUsage, errors.New("stdin is not a terminal — pass --yes to archive without a prompt")
		}
		ok, err := out.Confirm(os.Stdin, total, targets.Archive.Name)
		if err != nil {
			return exitUsage, err
		}
		if !ok {
			fmt.Println("Cancelled. Nothing moved.")
			return exitOK, nil
		}
	}

	fmt.Println()
	summary, runErr := archive.Run(ctx, client, session, targets, runOpts, journal, out.Batch)
	if journal != nil {
		journal.Finish(time.Now())
		if err := journal.SaveErr(); err != nil {
			out.Warnf("could not write the run journal: %v", err)
		}
	}
	out.Summary(summary, targets.Archive)

	switch {
	case runErr != nil && errors.Is(runErr, context.Canceled):
		fmt.Fprintln(os.Stderr, "\nInterrupted. Re-run the same command to pick up where this left off.")
		return exitPartial, nil
	case runErr != nil:
		return exitPartial, runErr
	case summary.Failed > 0:
		return exitPartial, nil
	}
	return exitOK, nil
}

func openJournal(session *jmap.Session, o archive.Options, t archive.Targets) (*archive.Journal, error) {
	dir, err := archive.StateDir()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(t.Sources))
	for _, mb := range t.Sources {
		names = append(names, mb.ID)
	}
	key := archive.JournalKey(session.AccountID, o, names)
	return archive.OpenJournal(dir, key, os.Args[1:], time.Now())
}

// parseBefore accepts YYYY-MM-DD, interpreted as local midnight — "before
// 2023-01-01" should mean the same thing to the tool as it does to the user —
// or a full RFC3339 timestamp when the exact instant matters.
func parseBefore(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("could not parse --before %q: use YYYY-MM-DD (e.g. 2023-01-01) or an RFC3339 timestamp", v)
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
