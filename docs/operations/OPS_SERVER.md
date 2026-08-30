# Akritas Web/API Runtime

## Purpose and Boundaries

`serve` turns the existing outbound OpenAI/RAG/MCP orchestration loop into a
local HTTP service for people and integrations:

```text
Web UI / Open WebUI / HTTP client
→ Akritas serve
→ fixed safety SYSTEM prompt
→ automatic BM25 search over the connected local knowledge base
→ an OpenAI-compatible model through an external /v1 endpoint
→ authorized read-only MCP tools when needed
→ response and tool activity
```

The Chat tab supports general questions, RAG and runbook search, log and metrics
diagnostics, and other tasks for which read-only tools are registered. The
Prepare change tab runs an isolated `simulate-change` operation against an
authorized repository workspace.

When `-rag-index` is set, the host runs `local.rag.search` for every new user
request before calling the model. Retrieval does not depend on whether a small
external model decides to invoke a tool. An empty BM25 result is also passed
explicitly: the model must distinguish missing local data from general
recommendations. This rule applies equally to runbooks, procedures,
configuration and architecture documentation, and other indexed materials.

Training, benchmarking, checkpoint conversion, and other administrative CLI
commands are not exposed through the Web API. Write and dangerous MCP tools are
also not exposed, even when present and authorized in the source MCP
configuration.

## Startup

Minimal general chat without tools:

```bash
./bin/akritas serve \
  -address 127.0.0.1:8090 \
  -base-url http://127.0.0.1:8080/v1
```

RAG, read-only MCP, and the test nftables workspace:

```bash
./bin/akritas serve \
  -address 127.0.0.1:8090 \
  -base-url http://127.0.0.1:8080/v1 \
  -rag-index artifacts/indexes/operations.tgr \
  -mcp-config data/mcp.json \
  -workspace nftables=testdata/ops-change/nftables \
  -max-tool-calls 8 \
  -request-timeout 10m
```

## Workspace Configuration and Shared Validators

A version-controlled example is available at
`configs/akritas/workspaces.example.json`. Create the local configuration
next to it:

```bash
cp configs/akritas/workspaces.example.json \
  configs/akritas/workspaces.local.json
```

`*.local.json` files in this directory are excluded from Git because a root can
contain local or internal absolute paths. Connect the configuration with:

```bash
./bin/akritas serve \
  -workspace-config configs/akritas/workspaces.local.json \
  -base-url http://127.0.0.1:8080/v1
```

Format:

```json
{
  "version": 1,
  "validators": ["go-vet", "yamllint"],
  "workspaces": [
    {
      "name": "backend",
      "root": "../../workspaces/backend",
      "validators": ["go-test"],
      "validator_mode": "append"
    },
    {
      "name": "isolated-go",
      "root": "/srv/repos/isolated-go",
      "validators": ["go-vet", "go-test"],
      "validator_mode": "replace"
    }
  ]
}
```

The top-level `validators` field is the shared list of executable profiles for
all workspaces. Built-in `go-format` and `json-syntax` checks operate
independently and cannot be disabled. Project-specific `validators` are
appended to the shared list by default (`validator_mode=append`). The `replace`
mode replaces the shared list, including global `-change-validator` values, but
does not replace the built-in safe checks.

`root` paths can be absolute or relative. A relative path is resolved from the
JSON configuration directory, not from the current shell directory. JSON
decoding is strict: unknown fields, unknown or duplicate profiles, duplicate
workspace names, and unsupported versions stop startup.

The repeatable legacy `-workspace name=directory` option remains available for
one-off runs and can be combined with the configuration when names do not
overlap. Repeatable `-change-validator` options add profiles to the shared
list. The model chooses only the change; the operator controls the workspace
catalog and validator policy.

Go validators begin with the `go` command from the `serve` process `PATH`, but
the active version and root are determined through `go env GOVERSION GOROOT` in
the operator's original environment. Checks then invoke
`<active GOROOT>/bin/go` directly with the same `GOROOT` and
`GOTOOLCHAIN=local`. Standard bootstrap-Go switching to a cached toolchain
therefore matches a manual run, while the sanitized environment does not need to
download or re-verify the `golang.org/toolchain` module. `go list`, `go vet`,
and `go test` run with `GOPROXY=off` and `GOSUMDB=off`. Restart Akritas after
changing Go, the Go environment, or `PATH`. Errors report the actual executable
and version.

Before connecting MCP, RAG, and the upstream model, `serve` runs preflight for
all declared validator profiles. It checks both the shared list and effective
workspace-specific lists after applying `append` or `replace`. Each executable
must be present in `PATH` and successfully report its version. For every
workspace with a Go validator, `go list -m` must succeed in the offline
environment. A preflight error stops startup so an invalid `PATH`, Go version,
or workspace root is detected before the first change request. Success prints
`Validator preflight: passed` with all discovered profiles and the number of
checked workspaces. When Go profiles are present, it also prints the pinned
version, for example `go_toolchain=go1.25.6`.

After startup, the Web UI is available at `http://127.0.0.1:8090/`.
`-workspace name=directory` can be repeated. The browser sends only the
workspace name, relative paths, and request text; the absolute root remains in
the server process and is not returned by the API.

The external model is discovered through `/v1/models` when `-upstream-model`
is empty. The upstream API key is read from `OPENAI_API_KEY`, or from the
variable selected by `-upstream-api-key-env`.

## Web UI

### Chat

The browser stores the current history in page memory and sends it in full to
`POST /api/v1/chat`. The response includes final text and tool activity:

```json
{
  "run_id": "run_0123456789abcdef0123456789abcdef",
  "model": "akritas",
  "answer": "...",
  "activity": [
    {"name": "local.rag.search", "id": "call_1", "status": "ok"}
  ],
  "capability_gaps": [
    {
      "step": "Check CPU usage over the last 30 minutes",
      "capability": "metrics.query_range",
      "reason": "No metrics tool is registered"
    }
  ]
}
```

The host builds `activity` from calls that were actually executed, making it
the source of truth for verification status. Missing capabilities use the
built-in read-only reporting tool `local.capability.report_missing`: the model
provides the step, proposed capability, and reason. The tool performs no
diagnostics and creates no external state; it only converts the model's finding
into the separate `capability_gaps` field. The UI displays diagnostic calls and
missing tools separately. If the model did not produce a report, the interface
says so explicitly and does not claim that no gaps exist.

Before the main tool loop, the host runs a separate capability-planning request
with only the reporting tool and `tool_choice=required`. The planner maps the
steps from the request or retrieved document to the catalog of real execution
tools and must return one structured report, including an empty `gaps` array
when nothing is missing. RAG and reporting tools are then excluded from the main
model-facing catalog: the model can invoke only real read-only diagnostic tools.
A successful `local.capability.report_missing` call confirms only that a gap
was recorded, not that the original step was performed.

The “show payload in the next response” checkbox adds raw arguments and
`ToolResult`. It affects the next request and does not retroactively reveal a
previous response. When no calls occurred, the UI states explicitly that no
payload is available. Debug output can expose internal documents, logs, and MCP
data, so it is disabled by default.

Assistant responses render as a safe Markdown subset: headings, paragraphs,
ordered and unordered lists, blockquotes, fenced code blocks, inline code, and
bold text. The renderer creates DOM nodes and writes user content through
`textContent`; model-provided HTML is not executed. No external JavaScript
libraries or CDNs are required, which keeps the UI operational on isolated
networks.

History is not yet stored on the server; reloading the page starts a new
conversation. Only one generation runs at a time so multiple users do not
overload the local inference server. Waiting requests are also bounded by the
shared `-request-timeout`.

### Prepare Change

The form accepts a workspace, request text, and an optional list of relative
files. With an empty list, the host performs bounded lexical discovery; an
explicit list remains an override. The model returns exact replacements through
a forced structured function. The host applies them in a one-time temporary
copy and builds the unified diff itself. Preview writes nothing. One model
repair attempt is allowed after an error.

No-op replacements are discarded with a warning. A new file is created only
through explicit `operation=create`: the target must not exist, its parent must
already exist inside the workspace, and Apply repeats a no-clobber check. An
empty `old` value without `create` remains an error and is not interpreted
heuristically.

Model-generated checks remain a non-executable checklist. Built-in
`go-format`/`json-syntax` checks and applicable executable profiles from the
shared or workspace list run in the temporary copy before approval is created.
The host skips profiles that do not apply to the changed file types. Results
contain name, status, files, command, bounded output, and duration; a failure
blocks preview.

After a successful preview, the UI displays a one-time approval button valid for
30 minutes. Before writing, the host verifies every source file byte for byte; a
stale or already-used preview is rejected. Commit and merge-request creation
remain manual. If both structured attempts are rejected, the UI shows the stage,
finish reason, token count, and function-argument size. The complete structured
proposal appears in a collapsible section and can be downloaded locally as
`akritas-proposal-attempt-N.json`; the Chat debug option is not required. A
bounded raw preview remains as a fallback for older API clients. The complete
proposal is not written to stderr.

## Alertmanager Webhook

`serve` accepts the standard Alertmanager webhook v4:

```text
POST /api/v1/alertmanager/webhook
```

When Akritas runs with an API key, the endpoint uses the same Bearer-token
protection as the other APIs. The host validates the version, group and alert
statuses, and RFC3339 timestamps. It limits one group to 128 alerts under the
shared 1 MiB HTTP limit. The payload is then converted into an untrusted
operational request and synchronously passes through the same RAG,
capability-planning, and read-only execution loop as Chat. Labels, annotations,
and URLs are treated as data, not instructions. A successful response contains
`run_id`, `answer`, `activity`, and `capability_gaps`; it does not include raw tool
payloads.

Manual invocation example:

```bash
curl --fail-with-body \
  -X POST http://127.0.0.1:8090/api/v1/alertmanager/webhook \
  -H 'Authorization: Bearer change-me' \
  -H 'Content-Type: application/json' \
  --data-binary '{
    "version": "4",
    "groupKey": "{}:{alertname=\"HighCPU\",instance=\"api-01\"}",
    "truncatedAlerts": 0,
    "status": "firing",
    "receiver": "akritas",
    "groupLabels": {"alertname": "HighCPU"},
    "commonLabels": {"alertname": "HighCPU", "severity": "critical"},
    "commonAnnotations": {"summary": "CPU usage is above 95%"},
    "routeLabels": {"team": "platform"},
    "externalURL": "http://alertmanager:9093",
    "notification_reason": "first notification",
    "alerts": [{
      "status": "firing",
      "labels": {
        "alertname": "HighCPU",
        "instance": "api-01",
        "severity": "critical"
      },
      "annotations": {
        "summary": "CPU usage is above 95%",
        "description": "api-01 CPU has exceeded 95% for 10 minutes"
      },
      "startsAt": "2026-08-23T10:00:00Z",
      "endsAt": "0001-01-01T00:00:00Z",
      "generatorURL": "http://prometheus:9090/graph?g0.expr=cpu",
      "fingerprint": "8f6c2b15a4d91e20"
    }]
  }'
```

Minimal Alertmanager configuration:

```yaml
receivers:
  - name: akritas
    webhook_configs:
      - url: http://akritas.internal:8090/api/v1/alertmanager/webhook
        send_resolved: true
        http_config:
          authorization:
            type: Bearer
            credentials: change-me
```

Processing is synchronous: Alertmanager considers only HTTP 2xx a successful
delivery and can retry a webhook after a 5xx response. The receiver `timeout`
must therefore account for Akritas `-request-timeout` and actual model speed.
Persistent deduplication by `groupKey` or `fingerprint` is not implemented
yet; repeated delivery causes repeated analysis.

## OpenAI-Compatible API

The following endpoints are available to Open WebUI and other clients:

```text
GET  /v1/models
GET  /v1/models/{model}
POST /v1/chat/completions
```

Client settings:

```text
Base URL  http://127.0.0.1:8090/v1
Model     akritas
API key   the AKRITAS_API_KEY value, or any value when authentication is disabled
```

`stream=false` and buffered SSE with `stream=true` are supported. The
response is fully generated together with the tool loop, then sent as a single
content chunk. Usage counters are zero because Akritas does not own the
external model's tokenizer.

## HTTP Endpoints

```text
GET  /                              embedded Web UI
GET  /health                        status, model, and tool/workspace counts
GET  /api/v1/workspaces             names of authorized workspaces
GET  /api/v1/runs                   latest durable runs, newest first
GET  /api/v1/runs/{id}              run lifecycle and bounded audit events
POST /api/v1/chat                   chat + activity + capability gaps + optional debug
POST /api/v1/alertmanager/webhook   Alertmanager v4 + RAG/read-only diagnostics
POST /api/v1/change/simulations     read-only proposed diff
POST /api/v1/change/simulations/{id}/apply  explicit one-time apply
GET  /v1/models                     OpenAI model catalog
POST /v1/chat/completions           OpenAI-compatible chat gateway
```

JSON requests are limited to 1 MiB; history is limited to 64 messages and
256 KiB. Response limits are configured with `-max-tokens` and
`-max-tokens-limit`.

## Authentication and Production Deployment

By default, the service listens only on `127.0.0.1` and does not require an API
key. Do not bind to an external interface without network isolation and
authentication.

When `AKRITAS_API_KEY` is set, `/api/v1/*` and `/v1/*` require:

```http
Authorization: Bearer <token>
```

Use `-api-key-env` to select a different variable. HTML and `/health` remain
available, but data and generation are protected. Team deployment requires a
trusted reverse proxy with TLS/SSO, request-rate limiting, and audit identity.
The current version does not implement SSO or per-user permissions. Actor
attribution is therefore `api-key` or `anonymous`, not an individual identity.

All HTTP `4xx/5xx` responses, host-validation errors, and rejected structured
attempts are written to stderr through the standard Go logger. Discarded no-op
edits are also logged as `change_warning`. Each model-generated check is logged
separately with `model_check_status=not_executed`, workspace, attempt number,
and index. This explicitly separates the model's proposed checklist from host
validators that actually ran. Checks are quoted, newlines are escaped, and
length is limited to 512 Unicode code points. Raw function arguments are not
written to server logs. Under systemd, logs are available through `journalctl`.

Successful and failed accepted executions are stored in the append-only JSONL
file selected by `-audit-log` (default `data/audit/akritas.jsonl`). An empty
value disables persistence. Each native response includes `run_id`; the
OpenAI-compatible endpoint returns it in `X-Akritas-Run-ID`. Audit records omit
prompts, model responses, tool arguments/results, approval IDs, authorization
headers, and API keys. The authenticated run endpoints accept `limit=1..1000`.

A repository workspace should point to the smallest necessary tree without
secrets. An authorized user can run discovery or explicitly submit a regular
file inside the root. Discovery blocks symlink escapes and common secret
filenames. API access also grants permission to press Apply for a registered
workspace.

## Verification

Unit and integration tests use a fake OpenAI server and verify:

1. the custom Chat API and OpenAI-compatible gateway;
2. mandatory Bearer tokens when authentication is enabled;
3. absence of absolute workspace roots from API responses;
4. read-only change snapshots;
5. complete exclusion of write and dangerous MCP definitions from the model tool catalog;
6. built-in validators, type-based skipping, and exclusion of secrets and VCS
   metadata from the validation copy;
7. strict workspace configuration, relative roots, and append/replace validator policy;
8. audit authentication, persistence, metadata redaction, lifecycle integrity,
   and corrupt-history rejection;
9. path and symlink escape rejection, approval expiry/replay, stale-source and
   create-race rejection, pessimistic MCP permissions, and tool timeouts.

The two repeatable end-to-end scenarios are documented under `demos/`.
