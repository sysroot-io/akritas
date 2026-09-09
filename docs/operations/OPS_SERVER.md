# Akritas Web/API Runtime

## Purpose and Boundaries

`serve` turns the existing outbound OpenAI/RAG/MCP orchestration loop into a
local HTTP service for people and integrations:

```text
Web UI / Open WebUI / HTTP client
→ Akritas serve
→ global instructions loaded from instructions/SYSTEM.md
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
  -base-url http://127.0.0.1:8080/v1 \
  -response-language en
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
  -max-iterations 12 \
  -max-tool-result-bytes 2097152 \
  -max-retrieved-context-bytes 131072 \
  -max-context-tokens 40000 \
  -max-model-tokens 16384 \
  -request-timeout 10m
```

## Service Configuration

Every `serve` flag can be configured through a versioned JSON file or an
environment variable. Resolution is deterministic, from highest to lowest
precedence:

1. command-line flag;
2. `AKRITAS_*` environment variable;
3. strict JSON service configuration;
4. built-in default.

Copy the complete example and select it with either the flag or environment:

```bash
cp configs/akritas/server.example.json \
  configs/akritas/server.local.json
./bin/akritas serve -config configs/akritas/server.local.json

# Equivalent service/container selection:
AKRITAS_CONFIG=/etc/akritas/server.json ./bin/akritas serve
```

`-config` overrides `AKRITAS_CONFIG`. The JSON decoder rejects unknown fields,
trailing values, unsupported versions, non-regular files, and files larger
than 1 MiB. Relative paths in the service configuration retain normal `serve`
semantics and are resolved from the process working directory; workspace roots
inside the separate workspace catalog remain relative to that catalog file.

| JSON key | Environment variable | CLI flag |
|---|---|---|
| `address` | `AKRITAS_ADDRESS` | `-address` |
| `base_url` | `AKRITAS_BASE_URL` | `-base-url` |
| `upstream_model` | `AKRITAS_UPSTREAM_MODEL` | `-upstream-model` |
| `model` | `AKRITAS_MODEL` | `-model` |
| `upstream_api_key_env` | `AKRITAS_UPSTREAM_API_KEY_ENV` | `-upstream-api-key-env` |
| `api_key_env` | `AKRITAS_API_KEY_ENV` | `-api-key-env` |
| `system_instructions` | `AKRITAS_SYSTEM_INSTRUCTIONS` | `-system-instructions` |
| `skills_dir` | `AKRITAS_SKILLS_DIR` | `-skills-dir` |
| `response_language` | `AKRITAS_RESPONSE_LANGUAGE` | `-response-language` |
| `rag_index` | `AKRITAS_RAG_INDEX` | `-rag-index` |
| `mcp_config` | `AKRITAS_MCP_CONFIG` | `-mcp-config` |
| `notifications_config` | `AKRITAS_NOTIFICATIONS_CONFIG` | `-notifications-config` |
| `alert_sources_config` | `AKRITAS_ALERT_SOURCES_CONFIG` | `-alert-sources-config` |
| `alert_store` | `AKRITAS_ALERT_STORE` | `-alert-store` |
| `public_url` | `AKRITAS_PUBLIC_URL` | `-public-url` |
| `workspace_config` | `AKRITAS_WORKSPACE_CONFIG` | `-workspace-config` |
| `audit_log` | `AKRITAS_AUDIT_LOG` | `-audit-log` |
| `search_top_k` | `AKRITAS_SEARCH_TOP_K` | `-search-top-k` |
| `result_runes` | `AKRITAS_RESULT_RUNES` | `-result-runes` |
| `max_tokens` | `AKRITAS_MAX_TOKENS` | `-max-tokens` |
| `max_tokens_limit` | `AKRITAS_MAX_TOKENS_LIMIT` | `-max-tokens-limit` |
| `max_tool_calls` | `AKRITAS_MAX_TOOL_CALLS` | `-max-tool-calls` |
| `max_iterations` | `AKRITAS_MAX_ITERATIONS` | `-max-iterations` |
| `max_tool_result_bytes` | `AKRITAS_MAX_TOOL_RESULT_BYTES` | `-max-tool-result-bytes` |
| `max_retrieved_context_bytes` | `AKRITAS_MAX_RETRIEVED_CONTEXT_BYTES` | `-max-retrieved-context-bytes` |
| `max_context_tokens` | `AKRITAS_MAX_CONTEXT_TOKENS` | `-max-context-tokens` |
| `max_model_tokens` | `AKRITAS_MAX_MODEL_TOKENS` | `-max-model-tokens` |
| `temperature` | `AKRITAS_TEMPERATURE` | `-temperature` |
| `request_timeout` | `AKRITAS_REQUEST_TIMEOUT` | `-request-timeout` |
| `workspaces` | `AKRITAS_WORKSPACES` | repeatable `-workspace` |
| `change_validators` | `AKRITAS_CHANGE_VALIDATORS` | repeatable `-change-validator` |

Duration values use Go duration syntax, such as `30s` or `10m`. List-valued
environment variables accept either comma-separated values or a JSON string
array. When a repeatable CLI flag is supplied for the first time, it replaces
the configured list; subsequent occurrences append to that CLI list.

The legacy `AKRITAS_UPSTREAM_BASE_URL` name remains accepted for compatibility,
but `AKRITAS_BASE_URL` takes precedence when both are set.

Do not place API tokens in the JSON file. `upstream_api_key_env` and
`api_key_env` contain environment-variable names; the actual default secrets
remain `OPENAI_API_KEY` and `AKRITAS_API_KEY`.

## Global Model Instructions

`serve` reads `instructions/SYSTEM.md` at startup and prepends its content to
the first system message of every upstream model request. This includes normal
chat, alert investigations, investigation planning, structured result
generation, repository discovery, and change preparation. Task-specific
instructions and the configured response-language rule follow the global file.

Select another file with:

```bash
./bin/akritas serve \
  -system-instructions /etc/akritas/SYSTEM.md \
  -base-url http://127.0.0.1:8080/v1
```

The path is resolved from the process working directory when it is relative.
The file must be a non-empty regular UTF-8 file no larger than 64 KiB. Akritas
loads it once during startup; restart the process after editing it. Missing or
invalid global instructions stop startup instead of falling back to rules
embedded in the binary.

The file controls model behavior only. It cannot add tools, authorize actions,
change Run budgets, or weaken host-side validation. Protect an operator-managed
copy with the same configuration-file permissions used for the service. The
container image includes the default file at
`/var/lib/akritas/instructions/SYSTEM.md`.

## Selective Operational Skills

`serve` loads an operator-controlled skill catalog from `skills` by default.
Select another directory with `-skills-dir <path>`, or use an empty value to
disable skills. Each immediate child directory is one skill:

```text
skills/
  postgresql/
    SKILL.md
  redis/
    SKILL.md
```

A skill without front matter uses its directory name as its only exact match
selector. Optional YAML-style front matter can add aliases:

```markdown
---
name: postgresql
description: Read-only PostgreSQL investigation guidance.
match:
  - postgres
  - postgresql
---
# PostgreSQL Investigation Skill

Check active queries, autovacuum workers, and waits when the corresponding
authorized tools are available.
```

`name` must equal the directory name. Skill names and selectors use lower-case
letters, digits, dots, underscores, and hyphens. The catalog accepts at most 128
skills, each file is limited to 64 KiB, total catalog input is limited to 512
KiB, each listable description is limited to 512 bytes, and no more than eight
skills are selected for one Run.

Automatic selection is host-owned and exact. Before investigation planning,
Akritas looks only at explicit `role`, `service`, `technology`, `component`,
`database`, `db`, `engine`, or `platform` fields in the latest request. After the initial
host-validated checks, it also extracts those fields from successful tools whose
names identify them as inventory or CMDB tools. Failed tool results do not
automatically select skills. Free-form mentions and retrieved RAG documents do
not trigger automatic selection.

When explicit facts are absent or an inventory/CMDB check is unavailable,
fails, or returns insufficient data, two host-owned read-only tools provide a
bounded fallback:

- `knowledge.list_skills` returns only skill names and short descriptions;
- `knowledge.load_skill` accepts one exact name returned by the catalog and
  adds that skill's trusted instructions to the current Run system context.

The planner can schedule these tools and they remain available to the adaptive
tool loop after an inventory failure. Every call consumes the normal Run tool
budget. Automatic and model-requested selections share the maximum of eight
skills per Run. Aliases are valid for automatic matching but cannot be passed
to `knowledge.load_skill`; this prevents a free-form lookup from resolving to
multiple files. Loading guidance is not evidence that the named technology is
actually installed or affected.

Selected skill content is appended to the host-owned system context before the
next model request. This lets an inventory result such as `role: postgresql`
load only `postgresql/SKILL.md` before the adaptive tool loop. The loop still
receives the normal authorized read-only tool schemas, so the skill can guide
the model toward available PostgreSQL checks without creating a capability.
Native chat responses report selected names in `skills`; alert investigations
persist the same names through their tool timeline and result context.

Skill files are trusted operator configuration and are loaded once at startup.
Restart Akritas after editing them. A skill affects reasoning only: MCP policy,
argument validation, tool timeouts, Run budgets, and write restrictions remain
host-enforced. The knowledge tools are registered only when the skill catalog is
enabled.

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

`-response-language` selects the language for model-generated human-readable
prose and structured fields such as investigation steps, reasons, summaries,
risks, and rollback guidance. It accepts a BCP 47 language tag, so it is not
limited to a built-in language list; examples include `en`, `ru`, `fr`, `de`,
`ja`, `zh-Hant`, and `pt-BR`. The default is `en`. The host keeps technical
identifiers, commands, paths, hostnames, metrics, labels, and evidence
references unchanged. A message written in another language does not override
the configured value.

This option does not localize the Web UI, fixed Markdown headings, API errors,
validation messages, or server logs; those remain English. Restart `serve`
after changing the flag.

Akritas supports both Chat Completions token-limit fields used by compatible
providers. It starts with `max_tokens` for legacy and local endpoints. If the
upstream explicitly rejects that field, Akritas retries once with
`max_completion_tokens` and remembers the accepted field for later requests.
The negotiation also works in the opposite direction after an upstream model
change. Unrelated HTTP errors are never retried by this compatibility path.

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
the source of truth for verification status. Before diagnostics, the model must
call the built-in read-only `local.investigation.submit_plan` tool exactly once.
Its structured payload separates executable `checks` from genuinely unavailable
`gaps`. Each check contains a step, an exact authorized tool name, arguments,
and a reason. Each gap contains a step, a proposed capability identifier, and a
reason. The UI presents activity as a Run log with the Run ID, tool status, call
ID, and a separate "Unavailable checks" section only when `capability_gaps`
contains at least one item. An accepted plan with no gaps is a normal status,
not an error.

Every registry execution also emits two safe structured log lines to stderr:
`component=akritas_tool event=start` and `component=akritas_tool event=finish`.
They contain the tool name, call ID, final status and duration in milliseconds,
but never tool arguments or results. Authenticated Run detail stores the
host-observed arguments and raw results for evidence review. Enable the Chat
debug checkbox only when the same payloads are needed in an immediate response.

The tools count in the page header is a button. It opens an authenticated
catalog containing every tool admitted to the Web runtime, including its name,
permission and description. The catalog intentionally omits input schemas and
credentials. It is also available from `GET /api/v1/tools`.

Before the adaptive tool loop, the host runs a separate investigation-planning
request with only the plan submission tool and `tool_choice=required`. The
planner receives the request, retrieved knowledge, the remaining check budget,
and the names, descriptions, and input schemas of authorized read-only execution
tools. It maps applicable runbook steps to exact tool arguments. Runbooks remain
human-first guidance: their prose never adds a capability.

The host rejects malformed plans, unknown or denied tool names, non-object
arguments, duplicate check/gap steps, and plans that exceed the remaining Run
budget. It then executes every accepted check through the normal registry,
policy, argument validator, timeout, and result-size controls. All resulting
`ToolResult` values are appended to model history before the final reasoning
pass. Failed tool results remain evidence of an attempted check, not of a
successful diagnosis. RAG and plan-submission tools are excluded from the
adaptive execution catalog; the model can make follow-up calls only to the same
authorized read-only diagnostic tools.

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

## Run Budgets

Every chat and alert investigation uses a host-owned budget. The model
cannot raise these limits. Akritas accounts for all model calls, authorized tool
calls, aggregate tool-result bytes, retrieved-context bytes, model output tokens,
submitted model-context bytes, and wall-clock duration.

`-max-context-tokens` is enforced using a conservative upper bound of one token
per serialized input byte. This remains safe when an upstream uses an unknown
tokenizer. When the upstream omits completion-token usage, Akritas charges the
entire reserved output allowance instead of assuming zero usage.

Budget exhaustion stops the Run with an error. Native chat responses include
`budget.limits` and `budget.usage`; background alert Runs persist bounded usage
counters. The Web UI displays current usage next to Chat tool activity.

For alert Runs, the validated `investigation` object is stored as a separate
durable audit record and is restored with the Run after restart.

## Incoming Alerts

The complete canonical schema and lifecycle contract is documented in
[`ALERT_CONTRACT.md`](ALERT_CONTRACT.md).

Akritas exposes one provider-neutral route:

```text
POST /api/v1/alerts/{source}/webhook
```

`{source}` selects an operator-configured adapter and authentication policy.
Built-in adapter types are `generic`, `alertmanager`, `uptime-kuma`, and
`pingdom`. Copy the example and enable it from the service configuration:

```bash
cp configs/akritas/alerts.example.json configs/akritas/alerts.local.json
export AKRITAS_ALERT_SOURCES_CONFIG=configs/akritas/alerts.local.json
export AKRITAS_ALERT_STORE=data/audit/alerts.jsonl
export AKRITAS_ALERTMANAGER_TOKEN='replace-me'
```

Each source must configure exactly one or more authentication mechanisms, or
explicitly set `allow_unauthenticated: true` when authentication is enforced by
a trusted reverse proxy. Supported mechanisms are a Bearer token, an
`X-Akritas-Signature: sha256=<hex>` HMAC over the exact request body, and a
configured secret header. Secret values are read from named environment
variables and never from the request path or model output.

Additional sources use the same list. For example:

```json
{"name":"generic","type":"generic","bearer_token_env":"AKRITAS_GENERIC_ALERT_TOKEN"}
{"name":"uptime-kuma","type":"uptime-kuma","secret_header":"X-Akritas-Source-Token","secret_env":"AKRITAS_UPTIME_KUMA_WEBHOOK_TOKEN"}
{"name":"pingdom","type":"pingdom","secret_header":"X-Akritas-Source-Token","secret_env":"AKRITAS_PINGDOM_WEBHOOK_TOKEN"}
```

These are individual objects to place in `sources`; they are shown on separate
lines for clarity, not as a complete JSON document. Remove unused source
objects so startup never requires irrelevant secrets.

The adapter converts the provider payload to the version 1 canonical contract.
Important fields are `state`, `name`, `severity`, `summary`, `entity`, provider
identity, timestamps, labels, annotations, links, `deduplication_key`, and
`correlation_key`. Provider strings remain untrusted data. A generic source
accepts a strict canonical batch:

```json
{
  "schema_version": 1,
  "alerts": [{
    "schema_version": 1,
    "state": "firing",
    "name": "HighCPU",
    "severity": "critical",
    "summary": "CPU usage is above 95%",
    "entity": {"kind": "host", "id": "pg01"},
    "started_at": "2026-09-04T08:00:00Z",
    "observed_at": "2026-09-04T08:01:00Z"
  }]
}
```

Akritas writes the delivery, normalized events, incident updates, and pending
jobs to the append-only alert store before returning. New events return HTTP
`202`; a fully duplicate delivery returns HTTP `200`. The response contains a
`delivery_id` and, for each input event, its `event_id`, `incident_id`, optional
`job_id`, and `accepted` or `duplicate` status.

Firing events open or update an incident. Only the first firing event for an
open correlation key creates an investigation job. Resolved events close the
incident. Deduplication and correlation survive restart. The in-process worker
uses the same binary, generation slot, RAG, selective skills, inventory/CMDB
tools, and read-only tool policy as Chat. Interrupted jobs are returned to
`pending` at startup. This design keeps the job boundary explicit so an
external queue/worker can replace the in-process worker later.

The legacy authenticated endpoint remains as a compatibility alias:

```text
POST /api/v1/alertmanager/webhook
```

It accepts Alertmanager webhook v4, uses the global Akritas Bearer API key, and
feeds the same durable pipeline under the source name `alertmanager`.

Investigation results contain `confirmed`, `suspected`, or `inconclusive`
finding status, actionability, descriptive confidence, summary, hypothesis,
impact, host-owned evidence references, ruled-out hypotheses, affected
components, recommended actions, and `production_writes: 0`. Confidence never
authorizes an operation.

Use `AKRITAS_PUBLIC_URL` (or `public_url`) to add a direct `/runs/{id}` link to
Telegram and Mattermost notifications. Full Runs, raw tool evidence, and
follow-up questions remain behind the regular Akritas API authentication.

## Notifications and Bot Interfaces

After successful structuring and host validation, Akritas can send an alert
investigation result to one or more channels:

- `webhook` - generic JSON webhook with optional Bearer authentication and an
  HMAC-SHA256 signature;
- `telegram` - a Bot API `sendMessage` call from a Telegram bot;
- `mattermost` - a REST API post from a Mattermost bot account.

Notifications are produced by alert investigation Runs, not ordinary Chat or
Prepare change. Receivers run in parallel, once each, with a shared configurable
timeout. There is no persistent outbox or automatic retry. One channel failure
does not stop the others or fail the already completed investigation. Every
attempt is recorded as a `notification_delivery` audit event.

Setup:

```bash
cp configs/akritas/notifications.example.json \
  configs/akritas/notifications.local.json

export AKRITAS_NOTIFICATIONS_CONFIG=configs/akritas/notifications.local.json
export AKRITAS_INCIDENT_WEBHOOK_URL=https://automation.example/hooks/akritas
export AKRITAS_INCIDENT_WEBHOOK_TOKEN='...'
export AKRITAS_INCIDENT_WEBHOOK_HMAC_SECRET='...'
export AKRITAS_TELEGRAM_BOT_TOKEN='...'
export AKRITAS_TELEGRAM_CHAT_ID='-1001234567890'
export AKRITAS_TELEGRAM_WEBHOOK_SECRET='...'
export AKRITAS_TELEGRAM_ALLOWED_CHAT_IDS='-1001234567890'
export AKRITAS_TELEGRAM_ALLOWED_USER_IDS='123456789'
export AKRITAS_MATTERMOST_URL=https://mattermost.example
export AKRITAS_MATTERMOST_BOT_TOKEN='...'
export AKRITAS_MATTERMOST_CHANNEL_ID='channel-id'
export AKRITAS_MATTERMOST_OUTGOING_WEBHOOK_TOKEN='...'
export AKRITAS_MATTERMOST_ALLOWED_CHANNEL_IDS='channel-id'
export AKRITAS_MATTERMOST_ALLOWED_USER_IDS='user-id'

./bin/akritas serve -base-url http://127.0.0.1:8080/v1
```

The file uses strict JSON schema version 1, is limited to 1 MiB, and contains 1
to 16 receivers. Unknown fields, duplicate names, and missing environment
variables stop startup. `timeout` uses Go duration syntax, defaults to `10s`,
and cannot exceed one minute. Complete example:

```json
{
  "version": 1,
  "timeout": "10s",
  "bot_queue_size": 64,
  "bot_history_messages": 20,
  "bot_history_bytes": 65536,
  "bot_session_ttl": "24h",
  "bot_max_sessions": 256,
  "receivers": [
    {
      "name": "automation",
      "type": "webhook",
      "url_env": "AKRITAS_INCIDENT_WEBHOOK_URL",
      "bearer_token_env": "AKRITAS_INCIDENT_WEBHOOK_TOKEN",
      "hmac_secret_env": "AKRITAS_INCIDENT_WEBHOOK_HMAC_SECRET"
    },
    {
      "name": "telegram-ops",
      "type": "telegram",
      "bot_token_env": "AKRITAS_TELEGRAM_BOT_TOKEN",
      "chat_id_env": "AKRITAS_TELEGRAM_CHAT_ID",
      "inbound_mode": "polling"
    },
    {
      "name": "mattermost-ops",
      "type": "mattermost",
      "base_url_env": "AKRITAS_MATTERMOST_URL",
      "bot_token_env": "AKRITAS_MATTERMOST_BOT_TOKEN",
      "channel_id_env": "AKRITAS_MATTERMOST_CHANNEL_ID",
      "inbound_secret_env": "AKRITAS_MATTERMOST_OUTGOING_WEBHOOK_TOKEN",
      "allowed_conversation_ids_env": "AKRITAS_MATTERMOST_ALLOWED_CHANNEL_IDS",
      "allowed_user_ids_env": "AKRITAS_MATTERMOST_ALLOWED_USER_IDS"
    }
  ]
}
```

For a generic webhook, `url` or `url_env` is required. `bearer_token_env` and
`hmac_secret_env` are optional and can be enabled together. Akritas sends
`Content-Type: application/json`, `X-Akritas-Event`, `X-Akritas-Delivery`, an
optional `X-Akritas-Run-ID`, and `X-Akritas-Signature: sha256=<hex>`. The
signature covers the exact request-body bytes. The envelope is limited to
512 KiB:

```json
{
  "version": 1,
  "event": "incident.investigated",
  "delivery_id": "...",
  "created_at": "2026-09-02T19:00:00Z",
  "incident": {
    "run_id": "run_...",
    "model": "akritas",
    "alert_status": "firing",
    "group_key": "...",
    "answer": "...",
    "skills": ["postgresql"],
    "activity": [],
    "capability_gaps": [],
    "investigation": {},
    "budget": {}
  }
}
```

Telegram requires `bot_token_env` and `chat_id` or `chat_id_env`.
`message_thread_id` or `message_thread_id_env` sends to a topic. `base_url` or
`base_url_env` is needed only for an alternative Bot API server and defaults to
`https://api.telegram.org`. A bot cannot initiate a private conversation; the
user must first message it or add it to the target group.

Mattermost requires `base_url` or `base_url_env`, `bot_token_env`, and
`channel_id` or `channel_id_env`. This is a bot-account token, not an incoming
webhook. The bot account must belong to the target team/channel and be allowed
to create posts.

Telegram and Mattermost receive compact fixed-format text: status, finding,
actionability, confidence, Run ID and link, alert/entity, summary, impact,
evidence references, ruled-out hypotheses, affected components, recommended
actions, and production-write count. Raw answers and tool payloads are not sent
to chat. Akritas neutralizes `@`, collapses line breaks in fields, and escapes
Mattermost Markdown. Telegram messages are limited to 4000 Unicode characters
and Mattermost messages to 16000, with explicit truncation markers.

## Conversations Through Telegram and Mattermost

A `telegram` receiver becomes bidirectional with `inbound_mode: "polling"` or
`inbound_mode: "webhook"`. If `inbound_mode` is omitted, the presence of
`inbound_secret_env` continues to enable webhook mode for compatibility.
Mattermost supports webhook ingress.

`allowed_conversation_ids` and `allowed_user_ids` can be written directly in
JSON or loaded from same-named `*_env` fields as a JSON array or comma-separated
list. For Telegram polling without an explicit allowlist, Akritas permits only
the outbound `chat_id`. Webhook mode requires at least one allowlist;
Mattermost requires a user allowlist that must not contain the bot's own ID.

### Telegram long polling

Recommended mode for a single-node service or container behind NAT:

```json
{
  "name": "telegram-ops",
  "type": "telegram",
  "bot_token_env": "AKRITAS_TELEGRAM_BOT_TOKEN",
  "chat_id_env": "AKRITAS_TELEGRAM_CHAT_ID",
  "inbound_mode": "polling"
}
```

Akritas calls Bot API `getUpdates` with a 30-second long-poll timeout and asks
only for `message` updates. No public ingress URL or
`AKRITAS_TELEGRAM_WEBHOOK_SECRET` is needed. After a temporary network error,
the poller retries with bounded exponential backoff from 1 to 30 seconds. The
offset advances only after an allowed message enters the local queue, so queue
pressure does not silently lose the update. The poller stops with the bot
gateway.

Telegram does not permit simultaneous `getUpdates` and webhook use. Akritas
does not delete a webhook automatically because that is an external
control-plane mutation. If one was previously configured, remove it manually
with `deleteWebhook`. Do not set `inbound_secret_env` in polling mode.

### Telegram Webhook and Mattermost Ingress

Inbound endpoints intentionally do not check the common `AKRITAS_API_KEY`, as
Telegram and Mattermost are not Akritas API clients. Each receiver instead uses
a provider-specific secret and allowlists:

```text
POST /api/v1/bots/telegram/{receiver}/webhook
POST /api/v1/bots/mattermost/{receiver}/webhook
```

For a Telegram webhook receiver, configure `"inbound_mode": "webhook"`,
`inbound_secret_env`, and an allowlist. The endpoint accepts a JSON `Update`,
checks `X-Telegram-Bot-Api-Secret-Token`, accepts only regular `message.text`,
and ignores senders with `is_bot=true`. Register the webhook yourself so
Akritas performs no external control-plane mutation at startup:

```bash
curl --fail-with-body \
  -X POST "https://api.telegram.org/bot${AKRITAS_TELEGRAM_BOT_TOKEN}/setWebhook" \
  --data-urlencode "url=https://akritas.example/api/v1/bots/telegram/telegram-ops/webhook" \
  --data-urlencode "secret_token=${AKRITAS_TELEGRAM_WEBHOOK_SECRET}" \
  --data-urlencode 'allowed_updates=["message"]'
```

Telegram's cloud webhook requires a public HTTPS endpoint on a supported
Telegram port. The reverse proxy must preserve the secret header. The
conversation key contains receiver, `chat.id`, and `message_thread_id`, so
topics have independent histories.

Mattermost ingress accepts `application/x-www-form-urlencoded` or JSON from an
outgoing webhook or slash command. Configure this callback in Mattermost:

```text
https://akritas.example/api/v1/bots/mattermost/mattermost-ops/webhook
```

The integration token must match the environment variable named by
`inbound_secret_env`. Outgoing webhooks work for public channels; a slash
command can be used in a private channel or direct message. Akritas replies
through the bot-account REST API rather than the synchronous webhook body, so
the user sees a post from the Mattermost bot. History is isolated by receiver
and `channel_id`.

After verification and parsing, the webhook handler quickly places the message
in a bounded in-memory queue and returns HTTP 200. The Telegram poller uses the
same queue and does not acknowledge an update with a new offset while the queue
is full. One worker performs model inference through the normal global
generation slot, preventing provider timeouts and duplicate inference. The
latest 4096 event IDs are suppressed in memory; a full webhook queue returns
503 so the provider can retry. Dedupe state and chat history disappear on
restart.

Session and queue settings have safe bounds:

| Field | Default | Allowed |
|---|---:|---:|
| `bot_queue_size` | `64` | `1..1024` |
| `bot_history_messages` | `20` | `2..64` |
| `bot_history_bytes` | `65536` | `1024..262144` |
| `bot_session_ttl` | `24h` | `1m..720h` |
| `bot_max_sessions` | `256` | `1..4096` |

Every successful conversation turn uses the same RAG, planning, skill, and
authorized read-only tool loop as `POST /api/v1/chat`. Only a delivered
user/assistant pair enters session history. The bot interface does not invoke
Prepare change, Apply, or other write endpoints.

Host commands do not call the model:

- `/help` and `/start` show usage;
- `/new` and `/reset` clear the current conversation history;
- `/status` shows the number of stored messages;
- `/ask <text>` explicitly submits a question; regular text does the same.

A long model answer is split into several provider messages instead of being
silently truncated. Replies are recorded as `bot_reply` audit events; model
Runs use source `telegram_bot` or `mattermost_bot`. Audit does not contain the
prompt, answer, provider secret, or conversation history.

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
GET  /runs                          investigation Run list UI
GET  /runs/{id}                     investigation Run detail and follow-up UI
GET  /health                        status, model, and tool/workspace counts
GET  /api/v1/tools                  authorized tool names, permissions, and descriptions
GET  /api/v1/workspaces             names of authorized workspaces
GET  /api/v1/runs                   latest durable runs, newest first
GET  /api/v1/runs/{id}              run lifecycle and bounded audit events
POST /api/v1/runs/{id}/chat         scoped follow-up; tool calls append to the Run
GET  /api/v1/incidents              correlated incidents, newest first
GET  /api/v1/incidents/{id}         incident with canonical events and jobs
GET  /api/v1/alert-events/{id}      one canonical alert event
GET  /api/v1/investigation-jobs/{id} investigation job status and Run ID
POST /api/v1/chat                   chat + activity + capability gaps + budget + optional debug
POST /api/v1/alerts/{source}/webhook configured canonical/provider alert ingress
POST /api/v1/alertmanager/webhook   authenticated Alertmanager compatibility alias
POST /api/v1/bots/telegram/{receiver}/webhook    authenticated Telegram text ingress
POST /api/v1/bots/mattermost/{receiver}/webhook  authenticated Mattermost text ingress
POST /api/v1/change/simulations     read-only proposed diff
POST /api/v1/change/simulations/{id}/apply  explicit one-time apply
GET  /v1/models                     OpenAI model catalog
POST /v1/chat/completions           OpenAI-compatible chat gateway
```

JSON requests are limited to 1 MiB; history is limited to 64 messages and
256 KiB. Per-response limits are configured with `-max-tokens` and
`-max-tokens-limit`. Aggregate Run limits use `-max-iterations`,
`-max-tool-calls`, `-max-tool-result-bytes`,
`-max-retrieved-context-bytes`, `-max-context-tokens`, and
`-max-model-tokens`.

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
prompts, model responses, approval IDs, authorization headers, and API keys.
Tool-call events retain arguments and raw results as host-owned evidence and are
therefore sensitive. The authenticated run endpoints accept `limit=1..1000`.

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
