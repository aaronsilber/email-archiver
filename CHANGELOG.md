# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] — 2026-08-05

Initial release.

### Added

- `--before DATE` archives Fastmail messages received strictly before a date by
  moving them into the account's Archive mailbox over JMAP. A bare `YYYY-MM-DD`
  is read as local midnight; an RFC3339 timestamp is taken as written.
- `--dry-run` prints per-mailbox counts and changes nothing. Without it, the
  counts are still shown first and a `[y/N]` prompt guards the move; `--yes`
  skips the prompt for non-interactive use.
- `--from MAILBOX`, repeatable, defaulting to the Inbox. Accepts a JMAP role,
  a full mailbox path, or a bare name when it is unambiguous.
- `--keep-unread` and `--keep-flagged` leave those messages where they are.
- `--batch N` (default 500), clamped to the server's advertised limits, plus
  `--verbose`, `--version`, and `--session-url`.
- Resumable, idempotent runs: the drain loop re-queries at position 0 each
  batch, so an interrupted run resumes by re-running the same command and a
  repeat run moves nothing. An advisory run journal under `$XDG_STATE_HOME`
  reports what earlier runs moved.
- Rate-limit handling: sequential requests, `Retry-After` honored, exponential
  backoff with jitter on 429 and 5xx, immediate failure on 401/403/404.
- Credentials from `$FASTMAIL_API_TOKEN` or `~/.config/email-archiver/config.toml`,
  which must be mode `0600`. There is no token flag.

### Safety

- No delete path exists: messages are only ever moved.
- Archive, Trash, Junk, and Drafts are refused as sources, before any message
  is touched.
- Moves are `mailboxIds` JSON patches only, so read state, flags, received
  dates, and other mailbox memberships are preserved.

[0.1.0]: https://github.com/aaronsilber/email-archiver/releases/tag/v0.1.0
