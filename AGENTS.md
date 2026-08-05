# email-archiver

## Purpose

CLI tool that archives Fastmail email older than a user-specified date.

"Archive" here means exactly what it means in Fastmail: move the message into the account's **Archive** mailbox. Same action as clicking Archive in the Fastmail web UI. The mail stays in the account, stays searchable, stays backed up by Fastmail — it just leaves the Inbox.

Nothing is ever deleted from the server. This tool has no delete path.

It is also not a mail client. No reading, composing, or replying. One job: move old mail out of the Inbox and into Archive.

## Usage shape

```
email-archiver --before 2023-01-01
email-archiver --before 2023-01-01 --dry-run
email-archiver --before 2023-01-01 --from Inbox --from Sent
```

- `--before DATE` — required. Messages received strictly before this date.
- `--from MAILBOX` — source mailbox, repeatable. Default: Inbox only.
- `--dry-run` — report what would move, change nothing.
- `--keep-unread` / `--keep-flagged` — leave those messages where they are.

## Constraints

- **Move only.** No delete, no expunge, no purge. Fastmail keeps the mail, so there is no need to copy anything locally first.
- **Leave the special mailboxes alone.** Never touch messages already in Archive, or anything in Trash, Junk, or Drafts.
- **Idempotent.** Re-running with the same arguments is a no-op — everything matching has already moved.
- **Resumable.** A 15-year Inbox can hold 100k+ messages. Work in batches, checkpoint progress, survive an interrupted run without starting over.
- **Preserve message state.** A move must not mark anything read, drop a flag, or alter the received date. Users should not be able to tell the difference between this tool and clicking Archive by hand.
- **Respect rate limits.** Batch requests, back off on 429, don't hammer the API.
- **Show the damage first.** Print per-mailbox counts of what matched before moving. `--dry-run` should be the natural first command anyone runs.

## Fastmail access

Two workable paths:

- **JMAP** (preferred) — `https://api.fastmail.com/jmap/session`, bearer auth with a Fastmail API token. `Email/query` with a `before` filter to find matches, then `Email/set` to rewrite `mailboxIds`. Native batching, so one round trip moves a whole batch.
- **IMAP** (fallback) — `imap.fastmail.com:993`, app-specific password. `SEARCH BEFORE` then `MOVE`. Simpler, more libraries, slower at scale.

Credentials come from an env var or a config file. Never from CLI arguments — they leak into shell history and `ps`. Never logged, never committed.

## Language

Open. Choose based on the quality of the available JMAP/IMAP libraries, not familiarity. Single-binary distribution is a plus; a script with a lockfile is fine too.

## Definition of done

A user with 15 years of Inbox runs one command, sees a count, confirms, and ends up with everything before the date sitting in Archive — flags intact, read state intact, and the account's total message count unchanged.

## Status

Implemented as of v0.1.0. Settled choices, all within the latitude above:

- **Go**, single binary, zero third-party dependencies — JMAP is JSON over HTTPS, so `net/http` and `encoding/json` cover it.
- **JMAP only.** The IMAP fallback was not built; it would double the surface area for no gain against Fastmail.
- **Confirmation prompt by default**, skippable with `--yes`. `--dry-run` never moves anything.

See [README.md](README.md) for usage and [docs/design.md](docs/design.md) for how the constraints above are actually enforced — in particular why the drain loop, not a checkpoint file, is what makes the tool idempotent and resumable.
