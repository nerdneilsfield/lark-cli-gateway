# File sending implementation plan

Readiness: ready. Authorization: implementation. Initial Git tree: clean.

## Outcome and scope
Send one client-local file through the gateway, including across an SSH tunnel.
Keep text/Markdown JSON unchanged and best-effort FIFO delivery. No URL fetching,
server-path input, batch files, media previews, persistent delivery, or status API.

## Design and constraints
- New `POST /send-file` accepts multipart fields `chat_id`, `as`, and exactly one
  `file`. Metadata is limited to 4 KiB per field, multipart overhead to 64 KiB.
  Reject unknown/duplicate fields, empty files, and unsafe filenames. Filenames
  stay plain basenames; preserve Unicode, spaces, and extensions.
- Stream uploads into private per-job directories. Default maximum file size is
  20 MiB (`-max-file-mb`, 1–1024); this is a gateway policy, not a claimed Lark limit.
  Allow two active uploads, reject excess with 503. The queue still bounds pending
  tasks; at most queue-size + 3 file payloads occupy disk (two uploads, one worker).
- Acquire the TCP listener before cleaning a gateway-owned cache directory keyed
  by the resolved listener address. This prevents a second server on the same
  endpoint from cleaning live uploads. Startup discards stale files; no recovery.
- Upload handler owns temporary storage until enqueue; worker owns it thereafter.
  Every failed upload/enqueue cleans it. Worker cleans after final success/failure.
  Internal jobs carry file directory/name/key; JSON never accepts server paths.
- One queue for text and files. FIFO means enqueue order, not upload-start order.
  A file blocks later messages. Run CLI in the job directory with `--file ./name`.
  Resolve the CLI executable before changing its cwd. Each file job has one random
  idempotency key reused across retries (CLI documents a one-hour window).
- HTTP upload read deadline: 5 minutes; header deadline: 10 seconds. CLI attempts
  are bounded to 5 minutes. Client HTTP timeout becomes 5 minutes. These bounds
  avoid unbounded occupancy; no retry of client HTTP requests after ambiguity.
- 200 means enqueued, 400 invalid multipart/metadata, 413 too large, 503 busy/full,
  500 temporary storage failure. Network failures after acceptance can be ambiguous.

## Acceptance map
| ID | Behavior | Owner | Check |
|---|---|---|---|
| A1 | Multipart bytes/name/identity reach queued file | T1 | HTTP handler test reads stored bytes and verifies job |
| A2 | Invalid, oversized, busy/full uploads do not leak | T1 | focused handler tests check status and empty spool |
| A3 | Retry reuses file/key; cleanup after completion; FIFO | T1 | worker tests with failing sender; existing FIFO tests |
| A4 | CLI uses relative path and correct cwd, attempt deadline | T1 | helper-process test records argv/cwd and timeout |
| A5 | Existing text/Markdown unchanged | T1/T2 | existing Go tests |
| A6 | Client sends local file bytes via multipart | T2 | httptest receiver verifies bytes/name/identity, stdout |
| A7 | Runnable client/server file path | T2 | built binaries plus fake lark-cli captures attachment bytes |

## Ordered tasks and progress
### T1: Gateway file intake and delivery
Status: done.
Files: cmd/lark-gateway-server/main.go and tests; new files.go and files_test.go;
README.md; this plan. Shared wire Message remains unchanged.
Implement bounded upload, internal job lifetime, command invocation and cleanup.
Check from repository root: go test ./..., make check, make build; targeted tests
above establish file behavior without sending a message to a real chat.
Commit one useful server endpoint with curl documentation immediately after checks.

### T2: Client file upload
Status: done. Depends on T1.
Files: cmd/lark-gateway-cli/main.go and tests; optional new upload.go; README.md.
Add mutually exclusive --file; stream multipart from a regular local file with
bounded memory and close/join the producer on HTTP errors. Keep host/as/env rules.
Check go test ./..., make check, and compiled client → server → fake CLI smoke.
Commit client feature and usage documentation after verification.

## Final acceptance
Fake CLI smoke must observe the exact client file bytes, preserved filename,
chat/as flags and idempotency key, followed by temporary-file removal. No live
Lark send without an explicitly supplied destination/content/identity. Platform
permissions and maximum upload size remain deployment prerequisites, not tested.

T1 evidence: `make check` and `make build` passed. Tests cover exact binary bytes,
Unicode filename, retry success/exhaustion cleanup, malformed/duplicate uploads,
size/full/busy rejection, stale cleanup, cwd/argv, deadline and existing FIFO.
README language check found only legitimate technical-format candidates.

T2 evidence: `make check`, `make build`, and `go test -race ./...` passed.
Compiled CLI posted a client-local binary file named `报告 1.pdf` to the compiled
server; a fake CLI observed exact bytes, name, user identity, relative path and
idempotency key. The job directory was absent after completion. Test processes
were stopped. Live Lark delivery remains untested; no destination was supplied.
All implementation tasks are complete. No push is authorized.
