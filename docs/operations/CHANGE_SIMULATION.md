# Isolated Infrastructure Change Preparation

## Purpose

`simulate-change` evaluates how an external OpenAI-compatible model chooses a
configuration change, but does not trust the model to construct a unified diff.
Files can be listed explicitly, or the host can automatically find a bounded set
of candidates. The model receives an immutable snapshot and returns only
structured replace/create operations:

```text
request + selected files
→ local host path and size validation
→ JSON snapshot in the prompt
→ forced function call with operation/path/old/new
→ strict JSON/schema validation
→ apply replacements in a one-time temporary workspace
→ host-generated unified diff
→ result with validation evidence
```

The source workspace is unchanged until explicit approval. The model receives no
filesystem, shell, Git, MCP, or production access. Its proposed verification
commands are displayed but never executed.

## Automatic File Selection

When the `-file` list or the Web UI file field is empty, the host requires the
model to return 2–8 short lexical queries through
`akritas_plan_repository_search`. The host then scans the workspace and ranks
UTF-8 documents by matches in their paths and contents. The model does not read
the directory and does not invent paths. At most 16 top candidates are included
in the snapshot, within the shared 256 KiB limit.

The scan is bounded to 10,000 text files and 64 MiB of read metadata and data.
The `.git`, `.hg`, `.svn`, `node_modules`, `vendor`, `dist`, `build`, and
`.cache` directories are skipped. `.env`, `.env.*`, private keys, and
`.pem`, `.key`, `.p12`, and `.pfx` files are not indexed. An explicit file
list remains an expert override and does not run the discovery planner.

## Model Contract

The OpenAI request contains only the `akritas_propose_edits` function and
`tool_choice=required`. Its arguments have this shape:

```json
{
  "analysis": "Why this source of truth was selected",
  "edits": [
    {
      "operation": "replace",
      "path": "pillar/prod/nftables.sls",
      "old": "exact unique fragment from the source file",
      "new": "replacement fragment"
    },
    {
      "operation": "create",
      "path": "salt/nftables/new_rule_test.go",
      "old": "",
      "new": "package nftables\n"
    }
  ],
  "checks": ["yamllint pillar/prod/nftables.sls"],
  "risks": ["An incorrect rule could expose the wrong backend"],
  "rollback": "Revert the pillar change and apply the previous state",
  "clarifications": []
}
```

The model does not provide hunk headers, line numbers, `index` hashes, or a
finished diff. For `replace`, the path must exactly match a path in the
snapshot and `old` must occur exactly once in the current content. A missing
`operation` is treated as `replace` for backward compatibility. Multiple
replacements in one file are applied sequentially.

A new file is identified only explicitly: `operation=create`, a missing
canonical relative path, `old=""`, and complete non-empty `new` content.
Inferring `create` from an empty `old` value is forbidden. The parent directory
must already exist inside the workspace; directory creation and protected secret
filenames are not supported.

When information is insufficient, the model must return `edits: []` and
non-empty `clarifications`. Questions are forbidden when edits are present, and
rollback is mandatory. Unknown JSON fields and limit violations are rejected.

`clarifications` contains only concrete unresolved questions for the user.
Explanations, conclusions, requirements, and assumptions belong in `analysis`.
A proposal with both non-empty `edits` and `clarifications` is therefore
rejected and receives a targeted repair instruction: either keep the edits and
clear the questions, or remove the edits and ask genuinely blocking questions.

For a change request, the existence of a similar configuration field,
aggregation, or internal function is not sufficient evidence that the requested
behavior is already implemented. The model must trace observable behavior from
the route or handler through the logic call and response or output. If the first
attempt returns `edits=[]` and `clarifications=[]`, the repair attempt receives
a separate instruction to propose exact edits or ask a concrete question about
the missing API contract, entry point, or source of truth. Diagnostics report
this refusal with a dedicated hint rather than only a generic schema-validation
error.

When a request asks for a new or additional API route, the model is instructed
to retain the existing endpoint and add a new one unless replacement is explicit.
For a response described as a “plain list,” it must inspect Go types and build
the required projection explicitly rather than returning an existing container
structure unchanged.

## Host Validation and Repair

The host performs the following checks:

1. the response is exactly one call to the required structured function;
2. JSON matches the strict schema;
3. a replace path is in the snapshot, while a create path is absent under a safe
   existing parent inside the workspace;
4. replace `old` is non-empty and has exactly one match; create has empty
   `old` and non-empty `new`;
5. the resulting file remains UTF-8 without NUL and stays within size limits;
6. the resulting snapshot remains within the shared limit;
7. changes are applied only in an `akritas-change-*` directory under the
   system temporary directory;
8. the host builds the diff from original and resulting content;
9. the temporary workspace is deleted and the source workspace is not written.

When exact `old` text is missing or occurs more than once, the error includes
the match count and up to eight starting lines (`matching_start_lines`). Repair
for `matches=0` requires copying current text from the snapshot again; repair
for `matches>1` requires extending `old` with unique context from the target
route, handler, function, or configuration section. The host never selects a
match arbitrarily and never replaces all matches.

The prompt also forbids guessing the semantics of special values: `0`, an
empty string, and `nil` are not automatically interpreted as “unlimited.” The
model must read the called function's implementation before making such a
change.

After an exact-replacement error or another validation error, the model receives
a concrete `HOST_VALIDATION_ERROR` and one retry. If the second proposal is
also invalid, the request fails and no questionable diff is shown.

On final failure, the Web API adds `error.details` for each attempt: stage
(`upstream`, `tool_call`, `decode_arguments`, `schema_validation`, or
`host_validation`), `finish_reason`, completion-token count, function name,
argument size, a bounded argument preview, and a diagnostic hint. The UI
displays this array directly under the error. The snapshot and request are not
copied into diagnostics; the model-response preview is limited to 4096 bytes.
The UI shows a short attempt card, while raw arguments are hidden by default in
the “Show raw model response” disclosure.

An empty `old` value in `replace` remains an error: a new file requires
explicit `operation=create`. A replacement with identical `old` and `new`
is safely removed from the proposal and added to `warnings`; remaining edits
continue. If no effective changes remain after removing no-ops, the proposal is
rejected with the count, edit index, and path of every discarded no-op. Targeted
repair requires a genuinely different `new` value or a concrete clarification,
not another fake diff.

For an HTTP request that explicitly requires non-JSON or plain text, the prompt
forbids using a JSON response helper or encoder. The model must extract the
elements of the required type, select the appropriate `Content-Type`, and
serialize the body deterministically. Other endpoints must remain unchanged.

`unexpected EOF` means that JSON in the function arguments ended before the
closing delimiter. When `finish_reason=length` appears at the same time, the
response definitely reached its limit: increase `-max-tokens` for `serve`
and, if needed, `-max-tokens-limit`, or reduce the snapshot by selecting files
explicitly. With another finish reason, the preview shows where the model
produced malformed JSON.

The host builds one valid hunk from the first to the last change in each file,
with three context lines at both ends. This can be wider than a minimal Myers
diff when several replacements are far apart, but line counts and paths are
computed deterministically rather than generated by the model.

Commands in model-generated `checks` are untrusted text and are never
executed. A separate host-validator pipeline automatically syntax-checks every
changed `.go` file, deterministically normalizes valid source through the
standard Go formatter before building the diff, and then verifies canonical
`gofmt`. Preview, approval, and later `go-vet` or `go-test` therefore work
with formatted content. The host adds a warning when normalization occurred. A
syntax error is not repaired automatically by the formatter; it blocks preview
and is sent to the model for the single repair attempt. Changed JSON is checked
separately for valid syntax.

Under `serve`, each model check is written to stderr as a separate event:

```text
level=info component=akritas workspace="backend" change_attempt=2 model_check_index=0 model_check_status=not_executed model_check="go test ./..."
```

Checks remain in per-attempt diagnostics even when the proposal is later
rejected by schema validation, host validation, or an executable validator.
Values are quoted and limited to 512 Unicode code points. This is operational
visibility, not command execution.

Complete structured tool-call arguments are retained in rejected-attempt API
diagnostics as `arguments`. The Web UI displays them in a “Show complete
structured proposal” disclosure and allows each
`akritas-proposal-attempt-N.json` file to be downloaded. The
`arguments_preview` field remains a bounded 4 KiB compatibility fallback. The
complete proposal is not printed to stderr; logs contain only its size, stage,
and concise error.

Executable profiles can be enabled explicitly for the Web runtime:

```bash
./bin/akritas serve \
  ... \
  -change-validator go-vet \
  -change-validator go-test \
  -change-validator yamllint
```

Profiles are normally configured in the shared `validators` list in
`configs/akritas/workspaces.local.json`, connected through
`-workspace-config`. Workspace-specific `validators` are appended to the
shared list or replace it with `validator_mode=replace`. The complete format
is documented in [`OPS_SERVER.md`](OPS_SERVER.md).

The host selects a profile only by the extensions of changed files. Commands and
arguments are fixed in the binary; the model cannot add a shell command. Each
command has a two-minute timeout, and combined stdout and stderr are limited to
64 KiB. Execution occurs in a one-time workspace copy with the proposal applied;
`.git`, build and cache directories, and common secret files are not copied.
Go receives `GOPROXY=off`, `GOSUMDB=off`, `CGO_ENABLED=0`, separate
HOME/TMP/GOCACHE directories, and an environment stripped of API keys.

The active runtime is resolved before environment sanitization by one
`go env GOVERSION GOROOT` command, matching the operator's manual execution.
The validator directly invokes `<active GOROOT>/bin/go`, sets the same
`GOROOT`, and pins `GOTOOLCHAIN=local` only after runtime selection. For
example, if bootstrap Go 1.21 selected 1.25.6 from the toolchain cache, the
binary from the 1.25.6 GOROOT is executed. It no longer needs to download or
verify `golang.org/toolchain` again while the checksum database is disabled.

Subsequent `go list`, `go vet`, and `go test` calls keep `GOPROXY=off` and
`GOSUMDB=off` and use the shared local module cache. Initial
GOVERSION/GOROOT resolution follows the operator's standard Go configuration
and is therefore equivalent to a manual run.

After a `go-vet` or `go-test` failure, the result reports the actual active Go
path, version, and local mode without another switch. Restart Akritas after
changing Go, the Go environment, or `PATH`.

At `serve` startup, preflight covers all validators declared in the shared
list, CLI options, and workspace-specific settings, including profiles from
`validator_mode=replace`. Each executable is actually invoked. Every workspace
with `go-vet` or `go-test` also receives a safe offline `go list -m` check.
Preflight does not run project tests or code, but detects a missing executable,
incompatible Go version, invalid module or workspace root, and unavailable
selected toolchain before serving HTTP. Any such error prevents the server from
starting.

`go-test` is deliberately opt-in: tests and package initialization execute code
from the model-proposed change. A temporary copy, sanitized environment, and
disabled Go proxy are not a complete operating-system or container sandbox and
do not prevent the program from opening network connections itself. Near
production, enable this profile only inside a separate container or VM security
boundary. `go-vet` and `yamllint` likewise require their executables in
`PATH`; a missing explicitly enabled validator is an error, not a successful
skip.

## Built-In nftables Fixture

`testdata/ops-change/nftables/` contains a synthetic scenario:

```text
REQUEST.md
inventory/backends.yaml
pillar/prod/nftables.sls
salt/nftables/README.md
salt/nftables/templates/backends.nft.jinja
```

The inventory contains `orders-v2` at `10.20.4.15:8443`, while the production
pillar contains only `orders-v1`. The README identifies the pillar as the
source of truth and forbids changing the shared Jinja template without a schema
change. The expected proposal is one replacement in
`pillar/prod/nftables.sls`.

## Running

From the `Akritas/` directory:

```bash
./bin/akritas simulate-change \
  -base-url http://127.0.0.1:8080/v1 \
  -root testdata/ops-change/nftables \
  -request REQUEST.md \
  -file inventory/backends.yaml \
  -file pillar/prod/nftables.sls \
  -file salt/nftables/README.md \
  -file salt/nftables/templates/backends.nft.jinja \
  -max-tokens 1600 \
  -temperature 0
```

Remove all `-file` options to let the model and host select files. The
`-print-prompt` option requires an explicit list because automatic discovery
itself makes a separate model call.

An empty `-model` selects the first model returned by `/v1/models`. The API
key is read from `OPENAI_API_KEY`; use `-api-key-env` to select another
variable. `-print-prompt` prints SYSTEM, USER, the tool schema, and
`tool_choice=required` without calling the model.

The result contains analysis, a fenced host-generated diff, host validation,
proposed but unexecuted model checks, host validators that actually ran, risks,
rollback, and the number of model attempts.

## Input Limits

With explicit selection, the host accepts only:

- 1 to 32 explicitly listed repository files;
- relative paths that remain inside the root after symlink resolution;
- regular UTF-8 files without NUL;
- at most 64 KiB per file;
- at most 256 KiB for the snapshot and request combined;
- up to 32 replacements and up to 16 items in each auxiliary list.

Duplicates, absolute paths, and traversal are rejected before the model is
called. The request and file contents are declared untrusted data. This is a
diagnostic boundary, not a sandbox for a hostile mutable repository; a
production implementation should read an immutable commit or isolated checkout.

## Web UI and API

The Prepare change tab in `serve` uses the same pipeline. The API returns both
a ready-to-render Markdown `answer` and a structured `result` with `diff`,
`changed_files`, `created_files`, `warnings`, `validation`, and `attempts`.
For a non-empty diff, the server creates a one-time pending approval valid for
30 minutes. The “Apply this diff” button calls a separate endpoint with
`confirm=true`. Before writing, the host rereads each source file and requires
a byte-for-byte match with the preview. A stale preview is rejected with HTTP
409.

For `create`, the host again requires that the target is absent immediately
before Apply and publishes a previously written temporary file through a
same-directory hard link that cannot overwrite a file that appeared
unexpectedly. Replace operations use temporary files in the same directories
and preserve permission bits. Multiple files use best-effort rollback after a
normal rename error; there is no absolute transaction guarantee if the process
crashes between renames. Approval is in-memory only, one-time, and disappears
after restart. Git commits and merge requests are not created automatically:
after application, the user inspects the working tree and commits through the
normal process.

