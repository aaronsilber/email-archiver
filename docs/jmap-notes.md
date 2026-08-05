# JMAP notes

The concrete wire format this tool depends on. `docs/design.md` covers *why*;
this file is *what*, so the reasoning there does not have to carry example
payloads.

JMAP is JSON over HTTPS, which is why this codebase has no third-party
dependencies: `net/http` and `encoding/json` cover all of it.

## Session

```
GET https://api.fastmail.com/jmap/session
Authorization: Bearer <token>
```

Response, trimmed to the fields `internal/jmap/session.go` reads:

```json
{
  "username": "you@fastmail.com",
  "apiUrl": "https://api.fastmail.com/jmap/api/",
  "capabilities": {
    "urn:ietf:params:jmap:core": {
      "maxObjectsInGet": 500,
      "maxObjectsInSet": 500,
      "maxCallsInRequest": 64,
      "maxSizeRequest": 10000000
    },
    "urn:ietf:params:jmap:mail": {}
  },
  "primaryAccounts": {
    "urn:ietf:params:jmap:mail": "u1234abcd"
  }
}
```

`primaryAccounts["urn:ietf:params:jmap:mail"]` is the `accountId` every
subsequent call needs. If it is missing, the token lacks the mail scope.

The limits are advisory but real: `maxObjectsInSet` caps a single `Email/set`,
and the tool clamps `--batch` to it. When a limit is absent the tool falls back
to conservative defaults (500 / 500 / 16).

## Request envelope

Every API call is a POST to `apiUrl`:

```json
{
  "using": ["urn:ietf:params:jmap:core", "urn:ietf:params:jmap:mail"],
  "methodCalls": [
    ["Mailbox/get", { "accountId": "u1234abcd", "ids": null }, "m0"]
  ]
}
```

Method calls are positional triples: `[name, arguments, callId]`. Responses
come back the same shape, in `methodResponses`, with the call ids echoed. A
method that fails is returned as `["error", {type, description}, callId]` —
which is why the client checks the *name* of each response, not just the HTTP
status.

## Mailbox/get

```json
["Mailbox/get", {
  "accountId": "u1234abcd",
  "ids": null,
  "properties": ["id", "name", "role", "parentId", "totalEmails"]
}, "m0"]
```

`role` is what identifies the special mailboxes; it is `null` for user-created
ones. The roles this tool cares about:

| Role | Use |
|---|---|
| `inbox` | default source |
| `archive` | destination; refused as a source |
| `trash`, `junk`, `drafts` | refused as sources |
| `sent` | ordinary source, only if asked for |

`parentId` builds the slash-separated path used to disambiguate two mailboxes
with the same name.

## Email/query

```json
["Email/query", {
  "accountId": "u1234abcd",
  "filter": {
    "operator": "AND",
    "conditions": [
      { "inMailbox": "mb-inbox" },
      { "before": "2023-01-01T05:00:00Z" },
      { "hasKeyword": "$seen" },
      { "notKeyword": "$flagged" }
    ]
  },
  "sort": [{ "property": "receivedAt", "isAscending": true }],
  "position": 0,
  "limit": 500,
  "calculateTotal": true
}, "q0"]
```

- `before` is a **UTCDate**: RFC3339, UTC, `Z` suffix, second precision. It
  compares against `receivedAt` and is **exclusive**, which is what "strictly
  before" in the spec means. `--before 2023-01-01` is parsed as local midnight
  and converted, so the timestamp above is what a US Eastern user sends.
- The last two conditions appear only with `--keep-unread` and `--keep-flagged`
  respectively. Note the inversion: *keeping* unread mail means querying for
  mail that `hasKeyword: "$seen"`.
- `calculateTotal` is what makes the pre-run count table possible. It is not
  free for the server, so the drain loop is the only other place it is used.

Response:

```json
["Email/query", {
  "accountId": "u1234abcd",
  "queryState": "...",
  "position": 0,
  "total": 41208,
  "ids": ["Mabc...", "Mdef..."]
}, "q0"]
```

`total` is omitted when `calculateTotal` is false, which is why the client
tracks it as a `*int` and reports `TotalKnown`.

## Email/set

```json
["Email/set", {
  "accountId": "u1234abcd",
  "update": {
    "Mabc...": {
      "mailboxIds/mb-inbox": null,
      "mailboxIds/mb-archive": true
    }
  }
}, "s0"]
```

Keys of the form `property/pointer` are JSON Patch pointers into the object.
`null` removes a mailbox, `true` adds one. Any other mailbox the message
belongs to is left alone, and because `keywords` never appears, read state and
flags are untouched.

No `ifInState` is sent: mail arriving mid-run would abort an otherwise valid
batch, and the response already reports per-message conflicts.

Response:

```json
["Email/set", {
  "accountId": "u1234abcd",
  "oldState": "...",
  "newState": "...",
  "updated": { "Mabc...": null },
  "notUpdated": {
    "Mdef...": { "type": "forbidden", "description": "..." }
  }
}, "s0"]
```

`updated` maps ids to `null` (or to server-set properties). `notUpdated` is the
per-message failure map — the run reports these and continues rather than
aborting, and excludes those ids from later batches so the loop terminates.

## Errors

**Request level.** Non-2xx responses carry a problem-details document:

```json
{ "type": "urn:ietf:params:jmap:error:limit", "limit": "rateLimit",
  "status": 429, "detail": "Too many requests" }
```

429 is retried, honoring `Retry-After` (delay-seconds or HTTP-date). 5xx and
transport errors are retried with exponential backoff. 401/403/404 are fatal.

**Method level.** `["error", {"type": "invalidArguments", ...}, "q0"]` inside a
2xx response. Retrying will not help, so it is surfaced immediately.

## Notes and gotchas

- A 401 from Fastmail usually means the token is expired or lacks the **Mail**
  read+write scope. Read-only tokens can query but cannot move.
- `Email/query` results are only meaningful alongside `queryState`; this tool
  sidesteps state entirely by re-querying from position 0 every batch rather
  than paging. See `docs/design.md`.
- Back-references (`{"resultOf": "q0", "name": "Email/query", "path": "/ids"}`)
  can fill a whole argument but cannot build `update`'s id-keyed map, so query
  and set cannot share one request.
- `internal/jmaptest` implements this same subset in memory, including the
  filter tree, patch semantics, `maxObjectsInSet` enforcement, and injectable
  429/401/per-message failures. It is the reference for what the tool actually
  sends.
