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
chat, Alertmanager investigations, investigation planning, structured result
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
The native chat and Alertmanager responses report selected names in `skills`,
and the Web Run log displays them.

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
but never tool arguments or results. The Web Run log includes the same duration.
Enable the debug checkbox before a request only when raw arguments and results
are required.

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

Every chat and Alertmanager investigation uses a host-owned budget. The model
cannot raise these limits. Akritas accounts for all model calls, authorized tool
calls, aggregate tool-result bytes, retrieved-context bytes, model output tokens,
submitted model-context bytes, and wall-clock duration.

`-max-context-tokens` is enforced using a conservative upper bound of one token
per serialized input byte. This remains safe when an upstream uses an unknown
tokenizer. When the upstream omits completion-token usage, Akritas charges the
entire reserved output allowance instead of assuming zero usage.

Budget exhaustion stops the Run with an error. Native chat and Alertmanager
responses include `budget.limits` and `budget.usage`. Final audit metadata keeps
bounded counters, not prompts or tool payloads. The Web UI displays current
usage next to tool activity.

For Alertmanager Runs, the validated `investigation` object is stored as a
separate durable audit record and is restored with the Run after restart.

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
investigation-planning, and read-only execution loop as Chat. Labels, annotations,
and URLs are treated as data, not instructions. A successful response contains
`run_id`, `answer`, `activity`, `capability_gaps`, `investigation`, `notifications`, and `budget`;
it does not include raw tool payloads.

`investigation` is a strict host-validated object containing independent
`finding_status` and `actionability` fields, descriptive `confidence`, a summary,
an optional hypothesis, affected components, recommended actions, and evidence
references. Evidence references must match tool-call IDs created by the host.
Invented references and unknown JSON fields reject the structured result.
Confidence is never used as an authorization decision.

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

## Уведомления и bot interfaces

После успешной структуризации и host-валидации результата Alertmanager Akritas
может отправить его в один или несколько каналов:

- `webhook` — универсальный JSON webhook с optional Bearer authentication и
  подписью HMAC-SHA256;
- `telegram` — вызов Bot API `sendMessage` от имени Telegram-бота;
- `mattermost` — создание поста через REST API от имени Mattermost bot account.

Уведомления относятся только к Alertmanager Run. Обычный Chat и Prepare change
их не создают. Получатели вызываются параллельно, каждый только один раз и с
общим настраиваемым timeout. Persistent outbox и автоматических повторов пока
нет. Сбой одного канала не останавливает остальные и не меняет успешный ответ
Alertmanager на 5xx: иначе Alertmanager мог бы повторить всё дорогое
расследование. Результаты доставки возвращаются в массиве `notifications` и
записываются audit event `notification_delivery`.

Подключение:

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

Файл имеет strict JSON schema версии 1, ограничен 1 MiB и содержит от 1 до 16
получателей. Unknown fields, duplicate names и отсутствующие environment
variables останавливают startup. `timeout` использует Go duration syntax,
по умолчанию равен `10s` и не может превышать одну минуту. Полный пример:

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

Для generic webhook `url`/`url_env` обязателен. `bearer_token_env` и
`hmac_secret_env` optional, но оба могут применяться одновременно. Akritas
отправляет `Content-Type: application/json`, `X-Akritas-Event`,
`X-Akritas-Delivery`, optional `X-Akritas-Run-ID` и
`X-Akritas-Signature: sha256=<hex>`. Подпись вычисляется от точных bytes body.
Envelope ограничен 512 KiB:

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

Для Telegram обязательны `bot_token_env` и `chat_id`/`chat_id_env`.
`message_thread_id`/`message_thread_id_env` отправляет сообщение в topic;
`base_url`/`base_url_env` нужен только для альтернативного Bot API server и по
умолчанию равен `https://api.telegram.org`. Бот не может первым начать личный
диалог: пользователь должен написать ему либо добавить его в нужную группу.

Для Mattermost обязательны `base_url`/`base_url_env`, `bot_token_env` и
`channel_id`/`channel_id_env`. Это именно bot account token, а не incoming
webhook. Bot account должен состоять в нужной team/channel и иметь право
создавать посты.

Telegram и Mattermost получают компактный фиксированный текст: status,
finding, actionability, confidence, Run ID, group, summary, affected components
и recommended actions. Raw answer и tool payload в чат не отправляются.
Akritas нейтрализует `@`, схлопывает переносы в полях и экранирует Mattermost
Markdown. Telegram message ограничено 4000 Unicode symbols, Mattermost — 16000;
превышение заканчивается явной отметкой truncation.

## Диалоги через Telegram и Mattermost

Receiver типа `telegram` становится bidirectional при `inbound_mode: "polling"`
или `inbound_mode: "webhook"`. Если `inbound_mode` не указан, наличие
`inbound_secret_env` по-прежнему включает webhook для обратной совместимости.
Mattermost поддерживает webhook ingress.

Списки `allowed_conversation_ids` и `allowed_user_ids` можно указать
непосредственно в JSON либо получить из одноимённых `*_env` fields как JSON
array или comma-separated list. Для Telegram polling без явного allowlist
Akritas разрешает только `chat_id`, уже настроенный для исходящих уведомлений.
Для webhook хотя бы один allowlist обязателен; для Mattermost обязателен user
allowlist, который не должен содержать user ID самого бота.

### Telegram long polling

Рекомендуемый режим для single-node service и container за NAT:

```json
{
  "name": "telegram-ops",
  "type": "telegram",
  "bot_token_env": "AKRITAS_TELEGRAM_BOT_TOKEN",
  "chat_id_env": "AKRITAS_TELEGRAM_CHAT_ID",
  "inbound_mode": "polling"
}
```

Akritas вызывает Bot API `getUpdates` с long-poll timeout 30 секунд и получает
только updates типа `message`. Public ingress URL и
`AKRITAS_TELEGRAM_WEBHOOK_SECRET` не нужны. После временной сетевой ошибки
poller повторяет запрос с bounded exponential backoff от 1 до 30 секунд.
Offset продвигается только после успешной постановки разрешённого сообщения в
локальную очередь; поэтому при перегрузке update не теряется. Poller завершается
вместе с bot gateway.

Telegram не разрешает одновременно использовать `getUpdates` и webhook. Akritas
не удаляет webhook автоматически, потому что это внешняя control-plane
операция. Если webhook был настроен раньше, удалите его вручную через
`deleteWebhook`. Для текущего polling config поле `inbound_secret_env` указывать
нельзя.

### Telegram webhook и Mattermost ingress

Входящие endpoints намеренно не проверяют общий `AKRITAS_API_KEY`, потому что
Telegram и Mattermost не являются Akritas API clients. Вместо него каждый
receiver использует provider-specific secret и allowlists:

```text
POST /api/v1/bots/telegram/{receiver}/webhook
POST /api/v1/bots/mattermost/{receiver}/webhook
```

Для Telegram webhook receiver укажите `"inbound_mode": "webhook"`,
`inbound_secret_env` и allowlist. Endpoint принимает JSON `Update`, проверяет header
`X-Telegram-Bot-Api-Secret-Token`, пропускает только обычное поле `message.text`
и игнорирует senders с `is_bot=true`. Настройте webhook самостоятельно, чтобы
Akritas не выполнял external control-plane mutations при startup:

```bash
curl --fail-with-body \
  -X POST "https://api.telegram.org/bot${AKRITAS_TELEGRAM_BOT_TOKEN}/setWebhook" \
  --data-urlencode "url=https://akritas.example/api/v1/bots/telegram/telegram-ops/webhook" \
  --data-urlencode "secret_token=${AKRITAS_TELEGRAM_WEBHOOK_SECRET}" \
  --data-urlencode 'allowed_updates=["message"]'
```

Telegram cloud webhook требует публичный HTTPS endpoint на поддерживаемом
Telegram port. Reverse proxy должен сохранить secret header. Conversation key
состоит из receiver, `chat.id` и `message_thread_id`, поэтому topics имеют
независимую историю.

Mattermost ingress принимает `application/x-www-form-urlencoded` или JSON от
outgoing webhook/slash command. В Mattermost укажите callback:

```text
https://akritas.example/api/v1/bots/mattermost/mattermost-ops/webhook
```

Token созданной integration должен совпадать с переменной из
`inbound_secret_env`. Outgoing webhooks подходят для public channels; slash
command можно использовать в private channel или direct message. Akritas
отвечает не синхронным webhook body, а через bot account REST API, поэтому
пользователь видит ответ именно от Mattermost bot. История разделяется по
receiver и `channel_id`.

Webhook handler после проверки и parsing быстро ставит сообщение в bounded
in-memory queue и отвечает HTTP 200. Telegram poller использует ту же очередь и
не подтверждает update новым offset, пока очередь заполнена. Model inference
выполняет один worker через обычный global generation slot. Это предотвращает
provider timeout и duplicate inference. Последние 4096 event IDs подавляются в
памяти; при полном webhook queue endpoint отвечает 503, чтобы provider мог
повторить update. Dedupe state и chat history исчезают после restart.

Настройки session/queue имеют безопасные bounds:

| Field | Default | Allowed |
|---|---:|---:|
| `bot_queue_size` | `64` | `1..1024` |
| `bot_history_messages` | `20` | `2..64` |
| `bot_history_bytes` | `65536` | `1024..262144` |
| `bot_session_ttl` | `24h` | `1m..720h` |
| `bot_max_sessions` | `256` | `1..4096` |

Каждая успешная conversation turn проходит тот же RAG, planning, skill и
authorized read-only tool loop, что `POST /api/v1/chat`. Только доставленная
пара user/assistant попадает в session history. Bot interface не вызывает
Prepare change, Apply и другие write endpoints.

Host commands не обращаются к модели:

- `/help` и `/start` показывают подсказку;
- `/new` и `/reset` удаляют history текущего conversation;
- `/status` показывает число сохранённых messages;
- `/ask <text>` явно отправляет вопрос; обычный text делает то же самое.

Длинный model answer делится на несколько provider messages вместо silent
truncation. Replies фиксируются как audit event `bot_reply`; model Runs имеют
source `telegram_bot` или `mattermost_bot`. Audit не содержит prompt, answer,
provider secret или conversation history.

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
GET  /api/v1/tools                  authorized tool names, permissions, and descriptions
GET  /api/v1/workspaces             names of authorized workspaces
GET  /api/v1/runs                   latest durable runs, newest first
GET  /api/v1/runs/{id}              run lifecycle and bounded audit events
POST /api/v1/chat                   chat + activity + capability gaps + budget + optional debug
POST /api/v1/alertmanager/webhook   Alertmanager v4 + structured investigation + notification statuses + budget
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
