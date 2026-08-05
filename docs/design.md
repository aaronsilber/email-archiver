# Design

Read this before changing anything in `internal/archive`. The tool is small,
but three of its guarantees — idempotence, resumability, and state preservation
— rest on two decisions that are easy to undo by accident.

## The drain loop

Every batch asks the same question, at the same offset:

```
Email/query {
  filter:   AND [ inMailbox: <source>, before: <cutoff>, ... ],
  sort:     [ receivedAt ascending ],
  position: 0,
  limit:    <batch size>,
  calculateTotal: true
}
```

Then it moves the returned ids and asks again. Position is always `0`. It never
pages.

That works because a moved message leaves the source mailbox and so stops
matching `inMailbox`. The result set shrinks by roughly one batch per round
trip until it is empty. `internal/archive/run.go` is that loop, and almost
everything the spec asked for falls out of it:

- **Idempotent.** A second run with the same arguments matches nothing, because
  everything that matched has already left the mailbox. There is no "already
  processed" bookkeeping to get wrong.
- **Resumable.** An interrupted run leaves the account in a valid intermediate
  state: some messages moved, the rest still matching. Re-running the identical
  command resumes exactly where it stopped. No checkpoint is consulted.
- **Correct under concurrent change.** Offset paging would be the obvious
  alternative and it is subtly wrong here: mail arriving or moving mid-run
  shifts the window, so page 2 can skip messages page 1 never saw. Re-querying
  from 0 cannot skip anything.

The one hazard is the mirror image of the same property: a message that
*fails* to move stays in the mailbox and therefore comes back on every
subsequent query. The loop tracks per-message failures and excludes them from
later batches; when a batch has no un-failed ids left, that mailbox is done.
Without that, one permanently locked message would spin forever.
`TestPerMessageFailureTerminates` guards it.

## The patch

A move is a JSON Patch on `mailboxIds`, and on nothing else:

```json
"update": {
  "<emailId>": {
    "mailboxIds/<sourceId>": null,
    "mailboxIds/<archiveId>": true
  }
}
```

Two things follow, both of which the spec requires:

- **Other mailboxes survive.** Replacing the whole `mailboxIds` object would
  drop every other mailbox the message belongs to — a user label, a second
  filed copy. Patch pointers touch only the two ids named.
- **State survives.** `keywords` is never sent, so `$seen` and `$flagged` are
  not modified. `receivedAt` is server-immutable. The result is
  indistinguishable from clicking Archive in the web UI, which is the whole
  point of the tool.

`TestMovePreservesState` asserts this on the wire, not just on the outcome: it
parses the recorded `Email/set` body and fails if any key outside
`mailboxIds/*` appears.

## Why two requests per batch

An obvious optimization is to send `Email/query` and `Email/set` in one HTTP
request, using a JMAP back-reference to feed the query's ids into the set.
It does not work here. A back-reference substitutes an entire argument value,
and `Email/set`'s `update` is an object *keyed by message id* — there is no way
to derive that map from a list of ids on the server side.

So each batch is two requests: query, then set. This costs a round trip per
batch, not per message, so at a 500-message batch it is not worth further
contortion.

The pre-run count is the case where batching does pay off: one `Email/query`
per source mailbox with `limit: 0, calculateTotal: true`, all packed into a
single request (`archive.Counts`), split only if the server's
`maxCallsInRequest` is smaller than the number of sources.

## Rate limits

The client is sequential — one request in flight at a time. With hundreds of
messages moving per round trip there is nothing to gain from concurrency, and
serial requests are the simplest possible way to stay inside a rate limit.

On a 429 the client honors `Retry-After` when present; otherwise it backs off
exponentially from 1s to a 32s cap, with jitter. 5xx and transport errors are
retried the same way. A 401, 403, or 404 is fatal immediately — retrying a bad
token six times only delays the error message.

Batch size is clamped to the server's advertised `maxObjectsInSet` and
`maxObjectsInGet` from the session document, so `--batch 100000` is silently
reduced rather than rejected by the server.

## Protected mailboxes

`Resolve` refuses any source whose role is `archive`, `trash`, `junk`, or
`drafts`, before a single message is touched. Archive is the destination;
the other three hold mail the user has already made a decision about, and
sweeping them into Archive would be a surprise the tool cannot undo.

Mailbox names resolve in a fixed order — role, then full path, then bare name —
and a bare name matching several mailboxes is an error listing the candidates.
The tool does not guess which `Receipts` you meant.

## The journal is advisory

`internal/archive/journal.go` writes a small JSON file per run under
`$XDG_STATE_HOME/email-archiver/`, keyed by a hash of the arguments that define
the run. It records how many messages have moved, so a resumed run can say
"an earlier run of this command moved 12,400 messages" instead of leaving the
user to guess.

It is deliberately not authoritative. Correctness comes from the drain loop.
A missing, corrupt, or unwritable journal produces a warning and the run
continues — losing an advisory file must never risk losing mail.

## What is deliberately absent

- **No delete path.** There is no `Email/set destroy` call anywhere in this
  codebase, and there should never be one.
- **No local mail copy.** Fastmail keeps the mail; copying it locally would
  create a second place for it to be wrong.
- **No `ifInState` on `Email/set`.** Mail arriving mid-run would abort an
  otherwise fine batch. Per-message errors already cover real conflicts.
- **No IMAP fallback.** JMAP batching moves a whole batch per round trip;
  a second backend would double the surface area for no gain on Fastmail.
- **No token flag.** Credentials come from the environment or a `0600` file,
  never from an argument that would land in shell history and `ps`.
