# Akritas

[![CI and Security](https://github.com/sysroot-io/Akritas/actions/workflows/ci.yml/badge.svg)](https://github.com/sysroot-io/Akritas/actions/workflows/ci.yml)
[![Documentation](https://img.shields.io/badge/docs-akritas.sysroot.io-2563eb)](https://akritas.sysroot.io/)
[![Go 1.22+](https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)

Akritas is a standalone host for LLM-powered operational automation. It accepts
requests from people and Alertmanager, searches local context, invokes authorized
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

Go 1.22 or later is required. The module selects the security-patched Go 1.26.6 toolchain by default.

```bash
go test ./...
go build -o bin/akritas ./cmd/akritas
```

Basic startup with a local `llama-server`:

```bash
./bin/akritas serve \
  -address 127.0.0.1:8090 \
  -base-url http://127.0.0.1:8080/v1
```

After startup, the Web UI is available at `http://127.0.0.1:8090/`.

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

## Interfaces

- Web UI and native API: `http://127.0.0.1:8090/`;
- OpenAI-compatible facade: `/v1/models`, `/v1/chat/completions`;
- Alertmanager webhook: `POST /api/v1/alertmanager/webhook`;
- upstream LLM: any compatible `/v1`, including llama.cpp and OpenRouter;
- external actions: MCP stdio servers with an explicit permissions policy;
- changes: isolated preview, validators, and one-time approval;
- audit: authenticated `/api/v1/runs` list and detail endpoints.

Startup, webhook, and API details are documented in
`docs/operations/OPS_SERVER.md`; the change pipeline is documented in
`docs/operations/CHANGE_SIMULATION.md`; component boundaries are documented in
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

## Container Deployment

```bash
cp .env.example .env
# Set AKRITAS_API_KEY in .env.
docker compose up --build -d
```

The service is published on `127.0.0.1:8090`, runs as a non-root user with a
read-only container filesystem, and stores audit records in a named volume.
