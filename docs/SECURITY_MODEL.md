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
   copies only authorized read-only definitions into its execution registry.
   A forced investigation plan may select only from that filtered catalog; the
   host validates every selected tool and its arguments before execution.
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

Runbook and RAG text is guidance, not authority. Plain text such as an SSH or
restart instruction does not create a tool, grant a permission, or bypass a Run
budget. The model proposes a structured investigation plan from the catalog it
receives. The host rejects unavailable tools, excludes denied and non-read
tools, validates arguments with the registered tool definition, executes the
accepted checks, and records their results.

Workspace access is configured by operators. API users receive workspace names,
not absolute roots. Possession of API access currently grants access to every
configured workspace and the ability to apply a valid pending change. Per-user
workspace authorization is not implemented.

## Data Handling

- API keys and authorization headers must never enter audit events.
- Tool arguments and results are omitted from ordinary audit events; only
  bounded metadata and status are retained.
- The bundled VictoriaMetrics MCP adapter uses an operator-configured endpoint,
  tenant headers, and credential environment variables. Model tool arguments
  cannot select a host, set credentials, or invoke write APIs. HTTP duration and
  decompressed JSON response size are bounded.
- Run budgets are calculated and enforced by the host. Missing upstream token
  usage is charged conservatively rather than treated as zero.
- Structured investigation evidence can reference only host-created tool-call
  IDs. Model-provided confidence is never an authorization input.
- Debug responses can expose repository, RAG, log, or MCP data and are disabled
  by default.
- The VictoriaMetrics MCP `-debug` flag writes a bounded preview of failed HTTP
  response bodies to stderr. It omits request headers, credentials, query
  arguments, and successful bodies, but the preview can still contain
  operational or proxy-generated sensitive data. Debug logging is disabled by
  default and should be enabled only during troubleshooting.
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
- An expensive MetricsQL query can consume VictoriaMetrics resources until the
  configured HTTP timeout. Apply query limits and read-only tenant policy at
  VictoriaMetrics or its authentication proxy as well as in Akritas.
- VictoriaMetrics or an intermediary can place sensitive data in an error
  response. Operators who enable MCP debug logging must protect and expire the
  resulting stderr logs.
