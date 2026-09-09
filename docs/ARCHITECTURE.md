# Akritas Architecture

## Project Boundary

Akritas is responsible for orchestration and the safety of operational
workflows:

```text
person / monitoring source / OpenAI client
                 |
                 v
             Akritas host <---------> bot gateways / notifications
       /          |           \
  RAG/BM25    MCP adapters    change pipeline
       \          |           /
                 v
        OpenAI-compatible LLM
```

The model is a replaceable compute backend. It receives the prompt, retrieved
context, and tool schemas, but it does not read files or connect to production
on its own. The host remains the source of truth for actions that were actually
performed.

Global model behavior is operator-controlled through a bounded UTF-8 Markdown
file, `instructions/SYSTEM.md` by default. The OpenAI client prepends the loaded
content to the first system message of every model request. Task-specific
prompts remain coupled to their schemas and host-side implementations and
follow the global instructions. The file affects model behavior but does not
grant capabilities or alter host authorization.

Operational `SKILL.md` files form a separate bounded catalog. The host matches
skills against explicit request fields and successful inventory or CMDB facts.
Matched content is added to the system context before planning or adaptive
follow-up. If those facts are absent or insufficient, the read-only internal
tools `knowledge.list_skills` and `knowledge.load_skill` let the model inspect
bounded name/description metadata and load one exact catalog entry. Every load
counts against the Run tool budget and the shared eight-skill cap. Skill
selection does not change the authorized tool catalog. A skill can guide use of
an already available tool but cannot register or authorize one, and loading it
is not evidence that its technology is present.

The upstream model remains outside the project boundary. Akritas can use
`llama-server`, OpenRouter, or another OpenAI-compatible service without
depending on model-specific Go packages, checkpoints, or tokenizer formats.

The `serve` adapter assembles runtime settings through one versioned
configuration layer. Command-line flags override `AKRITAS_*` environment
variables, which override the strict JSON service configuration, which
overrides built-in defaults. The service configuration selects paths and the
names of credential variables; credential values stay in the process
environment and are not fields in the JSON schema.

## Stable Replacement Ports

- `OpenAI /v1` separates orchestration from a specific model.
- MCP separates the reasoning loop from log, metrics, inventory, and server systems.
- The alert adapter registry separates provider payloads from canonical incident processing.
- The workspace catalog separates the change pipeline from Git repository locations.
- Validator profiles separate the model proposal from actual project verification.
- Notification configuration separates investigation results from the generic webhook, Telegram, and Mattermost.

As the infrastructure grows, individual functions can be delegated to Rundeck,
StackStorm, Keep, or internal services. The preferred integration is an MCP
adapter or HTTP integration, rather than embedding their SDKs in the core. This
keeps the UI, model backend, RAG, and approval policy unchanged.

## Source Code Structure

```text
cmd/akritas/                 executable entry point
internal/cli/                 CLI commands, flags, and output
internal/web/                 HTTP API, Web UI, and OpenAI-compatible facade
internal/web/ops_web/         embedded Web UI static assets
internal/corpus/              corpus storage, import, and quality control
internal/rag/                 index construction, search, and the RAG tool
internal/skills/              bounded SKILL.md catalog and exact matching
internal/mcp/                 tool registry, transport, and MCP host
internal/victoriametrics/     bounded read-only VictoriaMetrics MCP tools
internal/change/              discovery, proposal, validators, and approval
internal/clients/openai/      OpenAI-compatible API client and tool loop
internal/audit/               durable run lifecycle and security event log
internal/alerts/              canonical alerts, provider adapters, correlation, jobs
internal/notifications/       incident delivery and bidirectional bot adapters
```

`cmd/akritas` depends only on the CLI adapter. Domain packages do not depend on
`internal/cli` or `internal/web`. Outer adapters assemble shared components
through exported types and functions; cyclic package dependencies are not
allowed. The repository root contains no application logic.

## Security Boundary

Read-only MCP tools are admitted to the regular chat loop only after explicit
configuration authorization. This runtime does not expose write or dangerous
tools to the model. Before the final reasoning pass, a forced planning request
maps the user request and retrieved runbook guidance to the authorized read-only
tool catalog, including each input schema. The host validates the structured
plan, executes every planned check within the Run budget, and supplies the
resulting evidence to the final model request. Runbook text can guide selection
but cannot register, authorize, or otherwise create a capability.

File-change preparation is separate: the model returns a
structured proposal, the host verifies exact replacements, creates a temporary
copy, runs validators, and builds the diff. Writing to the source workspace is
possible only after one-time approval and another byte-for-byte verification of
the original files.

Every accepted execution receives a random run ID when audit storage is enabled.
The append-only JSONL store records lifecycle and bounded security events.
Authenticated Run detail includes host-recorded tool arguments and raw results;
credentials, approval capabilities, prompts, and model transcripts are not
stored.

Chat and alert reasoning run under a host-owned budget that spans model
iterations, tool calls, context, tool results, model output, and duration. The
alert investigation flow converts the free-form answer into a strict
`InvestigationResult`. The host validates every evidence reference against
actual tool-call IDs before accepting the result. `confidence` is descriptive
and cannot authorize a tool call or workspace change. The validated result is
persisted as its own append-only audit record and restored during audit replay.

After validating an alert result, the host can deliver it to configured
outbound channels. The operator controls addresses, recipient identifiers, and
secret-variable names; alerts, runbooks, and the model cannot select a URL or
channel. Receivers run in parallel with individual timeouts. A delivery failure
is recorded in audit but does not turn an already successful investigation into
a retry of the model workflow.

Telegram and Mattermost can also act as bidirectional chat adapters. Telegram
receives updates through long polling or a provider-specific webhook;
Mattermost uses an outgoing webhook or slash command. The poller or webhook
handler applies operator allowlists before placing a message in the bounded
queue; webhooks also verify a separate ingress secret. A worker keeps bounded
in-memory history by provider, receiver, and conversation, invokes the same
read-only Chat loop, and replies through the provider API. Bot ingress does not
expose the change Apply API or gain additional tools or permissions.

Commit, push, and merge-request creation are outside the current trusted boundary.

The alert store is a separate append-only journal for deliveries, normalized
events, correlated incidents, and investigation jobs. The in-process worker
claims pending jobs sequentially. On restart it returns interrupted `running`
jobs to `pending`; a future external queue can replace this worker without
changing the canonical event or adapter contracts.
