# email-archiver

Move old Fastmail messages out of the Inbox and into the Archive mailbox.

"Archive" means exactly what it means in Fastmail: the message moves to the
**Archive** mailbox, the same as clicking Archive in the web UI. The mail stays
in the account, stays searchable, stays backed up. It just leaves the Inbox.

**Nothing is ever deleted.** There is no delete path in this tool — no expunge,
no purge, no local copy to lose. Read state, flags, received dates, and any
other mailbox a message belongs to all survive a move untouched.

## Install

```
go build -o email-archiver ./cmd/email-archiver
```

Or install it onto your `PATH`:

```
go install github.com/aaronsilber/email-archiver/cmd/email-archiver@latest
```

Requires Go 1.25 or newer. There are no third-party dependencies.

## Credentials

Create an API token in Fastmail under **Settings → Privacy & Security → 
Integrations → API tokens**, with the **Mail** scope set to read and write.
Read-only is not enough — moving a message is a write.

Give the token to the tool one of two ways:

```
export FASTMAIL_API_TOKEN='fmu1-...'
```

or in `~/.config/email-archiver/config.toml` (respects `XDG_CONFIG_HOME`):

```toml
api_token = "fmu1-..."
```

The file must not be readable by anyone but you — `chmod 600` it, or the tool
refuses to read it. There is deliberately no `--token` flag: command-line
arguments end up in shell history and in `ps` output.

## Use

Start with a dry run. It is the natural first command, and it changes nothing:

```
$ email-archiver --before 2015-01-01 --dry-run
Messages received before 2015-01-01 00:00 EST

  MAILBOX  MATCHING
  Inbox      41,208

Destination: Archive

Dry run: nothing moved. Re-run without --dry-run to archive 41,208 messages.
```

Drop `--dry-run` and it asks before touching anything:

```
$ email-archiver --before 2015-01-01
Messages received before 2015-01-01 00:00 EST

  MAILBOX  MATCHING
  Inbox      41,208

Destination: Archive

Move 41,208 messages to Archive? [y/N] y

  Inbox: moved 500 (40,708 remaining)
  Inbox: moved 1,000 (40,208 remaining)
  ...
  Inbox: moved 41,208 (0 remaining)

Moved 41,208 messages to Archive.
```

Interrupt it with Ctrl-C and nothing is lost — re-run the identical command and
it picks up where it stopped. Run it twice and the second run finds nothing to
do. Both properties come from the same place; see
[docs/design.md](docs/design.md).

More than one source mailbox:

```
email-archiver --before 2023-01-01 --from Inbox --from Sent
```

`--from` takes a role (`inbox`, `sent`), a mailbox name (`Receipts`), or a full
path when a bare name is ambiguous (`Clients/Receipts`).

## Options

| Flag | Meaning |
|---|---|
| `--before DATE` | Required. Archive messages received **strictly before** this date. `YYYY-MM-DD` is read as local midnight; an RFC3339 timestamp is taken as written. |
| `--from MAILBOX` | Source mailbox, repeatable. Default: Inbox. |
| `--dry-run` | Print the counts and stop. Changes nothing. |
| `--yes` | Skip the confirmation prompt. Required when stdin is not a terminal. |
| `--keep-unread` | Only move messages already marked read. |
| `--keep-flagged` | Leave flagged messages where they are. |
| `--batch N` | Messages per request. Default 500, clamped to the server's limit. |
| `--verbose` | Log every HTTP request and its status. |
| `--version` | Print the version and exit. |
| `--session-url` | JMAP session endpoint. Defaults to Fastmail's. |

Exit codes: `0` success, `1` some messages could not be moved or the run was
interrupted, `2` bad arguments, missing credentials, or a refused request.

## What it refuses to do

- **Delete.** No code path removes a message from the account.
- **Read Archive, Trash, Junk, or Drafts.** Passing one of those to `--from` is
  an error, checked before a single message is touched.
- **Take a credential from a flag.** Environment variable or `0600` file only.
- **Move without showing you the counts first**, unless you pass `--yes`.

## Troubleshooting

**`Fastmail rejected the API token (HTTP 401)`** — the token is wrong, expired,
or lacks the mail read+write scope. Check `--verbose` output to confirm which
source the token was read from.

**`this account has no Archive mailbox`** — the account has no mailbox with the
`archive` role. Create one in Fastmail; the tool will not invent a destination.

**`mailbox "Receipts" is ambiguous`** — two mailboxes share that name. Pass the
full path, e.g. `--from Clients/Receipts`. The tool will not guess.

**`stdin is not a terminal`** — you are running under cron or a pipe, where
there is nobody to answer the prompt. Pass `--yes` once you are satisfied with
what a `--dry-run` reported.

**`is readable by others (mode 0644)`** — `chmod 600` the config file.

**It looks stalled** — a 429 from Fastmail is retried with backoff, honoring
any `Retry-After` the server sends. Run with `--verbose` to watch the retries.

**Some messages could not be moved** — they are listed by id with the server's
reason, and the run continues past them. Re-run the same command to retry; the
messages that already moved will not be touched again.

## Documentation

- [docs/design.md](docs/design.md) — why the drain loop is what makes this
  idempotent, resumable, and safe. Read this before changing `internal/archive`.
- [docs/jmap-notes.md](docs/jmap-notes.md) — the concrete JMAP requests and
  responses involved.
- [AGENTS.md](AGENTS.md) — the original specification.

## Tests

```
go test ./...
```

The suite runs against an in-memory fake Fastmail (`internal/jmaptest`), so it
needs no account and no network. It asserts the properties that matter: the
drain loop terminates, a second run moves nothing, and an `Email/set` body
contains `mailboxIds` patches and nothing else.

## License

Public domain. This software is released under [the Unlicense](LICENSE) — copy,
modify, sell, or redistribute it for any purpose, with no conditions and no
attribution required.
