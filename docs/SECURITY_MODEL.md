# Security Model

## Objectives

Akritas must keep untrusted model output and external data from silently
becoming privileged operational actions. The host, not the model, is the source
of truth for retrieval, tool execution, validation, approval, and writes.

## Trusted Computing Base

The trusted computing base consists of the Akritas binary, its configuration,
the operating-system account, explicitly configured validator executables, the
reverse proxy or local network boundary, and the audit-storage directory.
Upstream models, MCP servers, repository content, RAG documents, HTTP payloads,
and Alertmanager fields are untrusted.

## Enforcement Points

1. HTTP authentication protects API and OpenAI-compatible routes when an API key
   is configured.
2. Request, history, tool, result, snapshot, and proposal sizes are bounded.
3. MCP tools require explicit configuration authorization. The Web runtime
   copies only read-only definitions into its execution registry.
4. Local retrieval exposes a fixed index and accepts no model-controlled file
   path.
5. Change proposals use a forced function and strict JSON decoding.
6. Replace operations require one exact unique source fragment. Create
   operations require an absent target under an existing safe parent.
7. Materialization and validators run against a temporary workspace copy.
8. Apply requires an unexpired one-time approval and repeats byte-level source
   checks immediately before publication.
9. Audit records persist lifecycle and security-relevant events without storing
   authorization headers or API keys.

## Permission Model

Tool permissions are `read`, `write`, and `dangerous`. Permission annotations
are interpreted pessimistically. A tool is callable only when its name is
allowed and its permission is within the active policy. The Web chat runtime
never publishes write or dangerous tools.

Workspace access is configured by operators. API users receive workspace names,
not absolute roots. Possession of API access currently grants access to every
configured workspace and the ability to apply a valid pending change. Per-user
workspace authorization is not implemented.

## Data Handling

- API keys and authorization headers must never enter audit events.
- Tool arguments and results are omitted from ordinary audit events; only
  bounded metadata and status are retained.
- Debug responses can expose repository, RAG, log, or MCP data and are disabled
  by default.
- Full rejected proposals are returned only in the request response and are not
  written to server logs.
- Audit storage is append-only from the application's perspective and must be
  protected by filesystem permissions and deployment backups.

## Validator Boundary

Built-in syntax checks operate on data. External validators execute programs.
`go-test` can run arbitrary code from the workspace and proposed change. Offline
Go settings and a sanitized environment reduce accidental access but do not
prevent direct network or operating-system access. Use a container or VM boundary
for executable validators near production.

## Residual Risks

- A compromised host account can modify workspaces and audit storage.
- Multi-file Apply uses best-effort rollback and is not crash-atomic.
- In-memory approvals disappear after restart.
- Authentication is instance-wide rather than per-user.
- Persistent audit records establish operational history, not cryptographic
  non-repudiation.
- A malicious authorized MCP server can return deceptive data or consume local
  resources within process and timeout limits.
