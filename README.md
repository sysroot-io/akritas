# Akritas

[![CI and Security](https://github.com/sysroot-io/Akritas/actions/workflows/ci.yml/badge.svg)](https://github.com/sysroot-io/Akritas/actions/workflows/ci.yml)
[![Documentation](https://img.shields.io/badge/docs-akritas.sysroot.io-2563eb)](https://akritas.sysroot.io/)
[![Go 1.25+](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

Akritas is a standalone host for LLM-powered operational automation. It accepts
requests from people and monitoring systems, searches local context, invokes authorized
MCP tools, prepares verifiable file changes, and shows the actions that were
actually performed.

The model does not receive direct access to production or a workspace. Akritas
performs all reads, tool calls, validators, and writes according to local policy.
Changing files requires separate approval; commits and merge requests remain
manual for now.

Every accepted operational execution can be written to a durable append-only
JSONL audit log. Audit records contain lifecycle, bounded metadata, and tool
outcomes, but omit prompts, tool payloads, model output, and credentials.

## Build and Test

Go 1.25 or later is required. The module selects the security-patched Go 1.26.6 toolchain by default.

```bash
go test ./...
go test -tags=smoke -v ./tests/smoke
go build -o bin/akritas ./cmd/akritas
```

The tagged smoke suite builds and launches the real binary against a controlled
OpenAI-compatible upstream, then verifies startup, authentication, the Web UI,
alert ingestion, asynchronous investigation, Run retrieval, and deduplication.

Basic startup with a local `llama-server`:

```bash
./bin/akritas serve \
  -address 127.0.0.1:8090 \
  -base-url http://127.0.0.1:8080/v1 \
  -response-language en
```

Every `serve` option can also be supplied through a strict JSON configuration
or an `AKRITAS_*` environment variable. Precedence is command-line flag,
environment, JSON configuration, then built-in default. Start from
`configs/akritas/server.example.json` and select it with `-config` or
`AKRITAS_CONFIG`; secrets remain in the environment variables named by
`upstream_api_key_env` and `api_key_env`.

After startup, the Web UI is available at `http://127.0.0.1:8090/`.
Akritas loads its global model behavior from `instructions/SYSTEM.md` before
accepting requests. Use `-system-instructions /path/to/SYSTEM.md` to select an
operator-managed file. Startup fails when the selected file is missing, empty,
invalid UTF-8, or larger than 64 KiB.

Operational skills live under `skills/<name>/SKILL.md`. `serve` loads the
catalog from `-skills-dir` (default `skills`). It automatically selects exact
matches from explicit `role`, `service`, `technology`, `component`, `database`,
`engine`, or `platform` fields and successful inventory/CMDB results. When that
evidence is unavailable or inconclusive, the model can list bounded metadata
with `knowledge.list_skills` and load one exact name with
`knowledge.load_skill`. Loaded guidance never adds a tool, authorizes an action,
or proves that a technology is present. Set `-skills-dir ''` to disable skills.

`-response-language` accepts a BCP 47 tag such as `ru`, `fr`, `ja`, or
`pt-BR` and controls model-generated prose only. The Web UI remains English.

The upstream can be replaced without rebuilding Akritas: any OpenAI-compatible
`/v1` endpoint is sufficient. For OpenRouter, set a key and explicitly select a
model:

```bash
export OPENAI_API_KEY='...'
./bin/akritas serve \
  -base-url https://openrouter.ai/api/v1 \
  -upstream-model qwen/qwen3.5-9b
```

## RAG from Local Documents

```bash
./bin/akritas import-local \
  -input /srv/runbooks \
  -source company-runbooks \
  -license internal \
  -output data/runbooks

./bin/akritas build-rag-index \
  -manifest data/runbooks/train/manifest.json \
  -output data/runbooks.tgr

./bin/akritas inspect-rag-index -load data/runbooks.tgr
./bin/akritas list-rag-documents -load data/runbooks.tgr
./bin/akritas search-rag -load data/runbooks.tgr -query 'high cpu' -top-k 5
```

Connect the index, MCP, and workspace catalog:

```bash
./bin/akritas serve \
  -base-url http://127.0.0.1:8080/v1 \
  -rag-index data/runbooks.tgr \
  -mcp-config configs/mcp.local.json \
  -workspace-config configs/akritas/workspaces.local.json
```

A version-controlled workspace configuration example is available at
`configs/akritas/workspaces.example.json`. Local configurations containing
internal paths or credentials are excluded from Git.

Completed alert investigations can be delivered simultaneously to a generic
HTTP webhook, Telegram, and Mattermost. Configure receivers with
`-notifications-config` or `AKRITAS_NOTIFICATIONS_CONFIG`; see
`configs/akritas/notifications.example.json`. Bot tokens and signing secrets
are read only from environment variables. Telegram long polling requires no
public endpoint; webhook mode remains available. Mattermost uses an outgoing
webhook or slash command. Both adapters pass user messages into the same Chat
investigation loop. History is isolated by Telegram chat/topic or Mattermost
channel, and `/new`, `/status`, and `/help` manage the session.

Provider-neutral alert ingestion is configured separately with
`-alert-sources-config` or `AKRITAS_ALERT_SOURCES_CONFIG`; see
`configs/akritas/alerts.example.json`. Built-in adapters support the canonical
Akritas contract, Alertmanager v4, Uptime Kuma, and Pingdom. Accepted events are
durably deduplicated and correlated before an in-process worker starts a Run.

## VictoriaMetrics MCP

Akritas includes a read-only stdio MCP server for VictoriaMetrics. It exposes
bounded instant queries, range queries, series discovery, label discovery, and
label-value discovery. The operator fixes the endpoint, tenant, credentials,
HTTP timeout, and response-size limit when starting the MCP process.

```bash
export VICTORIAMETRICS_BEARER_TOKEN='example-token'
./bin/akritas serve \
  -base-url http://127.0.0.1:8080/v1 \
  -mcp-config configs/mcp.victoriametrics.example.json
```

The example expects the binary at `/usr/local/bin/akritas`; adjust `command`
for a local build. For VictoriaMetrics cluster mode, set the MCP `-base-url` to
the tenant read root, for example
`http://vmselect:8481/select/0/prometheus`. Credentials are read from a named
environment variable and are never accepted as model-controlled tool arguments.

Add `-debug` to the `mcp-victoriametrics` arguments while diagnosing an
integration. Failed HTTP responses then log their status, content type, and a
2 KiB body preview to stderr. Request headers, credentials, query arguments,
and successful response bodies are not logged. Disable the flag after
troubleshooting because error bodies can contain operational data.

## Interfaces

- Web UI and native API: `http://127.0.0.1:8090/`;
- OpenAI-compatible facade: `/v1/models`, `/v1/chat/completions`;
- configured alert webhooks: `POST /api/v1/alerts/{source}/webhook`;
- Alertmanager compatibility webhook: `POST /api/v1/alertmanager/webhook`;
- outbound incident notifications: generic webhook, Telegram bot, and Mattermost bot;
- bot chat ingress: Telegram long polling or webhook, and Mattermost webhook endpoints;
- upstream LLM: any compatible `/v1`, including llama.cpp and OpenRouter;
- external actions: MCP stdio servers with an explicit permissions policy;
- metrics: bundled read-only VictoriaMetrics MCP adapter;
- changes: isolated preview, validators, and one-time approval;
- audit: `/runs`, `/runs/{id}`, authenticated Run/incident APIs, and scoped follow-up chat.

Startup, webhook, and API details are documented in
`docs/operations/OPS_SERVER.md`; the change pipeline is documented in
`docs/operations/CHANGE_SIMULATION.md`; the canonical monitoring contract is in
`docs/operations/ALERT_CONTRACT.md`; component boundaries are documented in
`docs/ARCHITECTURE.md`. See `docs/operations/DEPLOYMENT.md` for the hardened
single-node Compose deployment, and `SECURITY.md` for security reporting and
links to the security and threat models.

## Reproducible Demos

- `demos/incident-response` builds a local runbook index and processes a real
  Alertmanager v4 payload.
- `demos/safe-change` prepares, validates, reviews, and applies an exact
  replacement through one-time approval and stale-source checks.

Both demos use the regular binary and HTTP interfaces; neither bypasses host
policy or validation.

## License

Akritas is licensed under the [Apache License 2.0](LICENSE).

## Container Deployment

```bash
cp .env.example .env
# Set AKRITAS_API_KEY in .env.
docker compose up --build -d
```

The service is published on `127.0.0.1:8090`, runs as a non-root user with a
read-only container filesystem, and stores audit records in a named volume.
