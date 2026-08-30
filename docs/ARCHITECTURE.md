# Akritas Architecture

## Project Boundary

Akritas is responsible for orchestration and the safety of operational
workflows:

```text
person / Alertmanager / OpenAI client
                 |
                 v
             Akritas host
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

The upstream model remains outside the project boundary. Akritas can use
`llama-server`, OpenRouter, or another OpenAI-compatible service without
depending on model-specific Go packages, checkpoints, or tokenizer formats.

## Stable Replacement Ports

- `OpenAI /v1` separates orchestration from a specific model.
- MCP separates the reasoning loop from log, metrics, inventory, and server systems.
- The Alertmanager webhook separates the event source from incident processing.
- The workspace catalog separates the change pipeline from Git repository locations.
- Validator profiles separate the model proposal from actual project verification.

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
internal/mcp/                 tool registry, transport, and MCP host
internal/change/              discovery, proposal, validators, and approval
internal/clients/openai/      OpenAI-compatible API client and tool loop
internal/audit/               durable run lifecycle and security event log
```

`cmd/akritas` depends only on the CLI adapter. Domain packages do not depend on
`internal/cli` or `internal/web`. Outer adapters assemble shared components
through exported types and functions; cyclic package dependencies are not
allowed. The repository root contains no application logic.

## Security Boundary

Read-only MCP tools are admitted to the regular chat loop only after explicit
configuration authorization. This runtime does not expose write or dangerous
tools to the model. File-change preparation is separate: the model returns a
structured proposal, the host verifies exact replacements, creates a temporary
copy, runs validators, and builds the diff. Writing to the source workspace is
possible only after one-time approval and another byte-for-byte verification of
the original files.

Every accepted execution receives a random run ID when audit storage is enabled.
The append-only JSONL store records lifecycle and bounded security events. It
does not record prompts, model answers, tool arguments, tool results, approval
capabilities, or credentials.

Commit, push, and merge-request creation are outside the current trusted boundary.

## Next Boundaries to Stabilize

1. Add firing/resolved incident correlation above the generic run store.
2. Define a versioned provider interface for an external incident/task backend.
3. Add a separate policy for future write actions with two-phase approval.
