# Threat Model

## Scope

This model covers the single-node Akritas service, its HTTP interfaces,
OpenAI-compatible upstream, MCP stdio children, local RAG index, configured
repository workspaces, validator processes, and audit storage.

## Assets

- repository and configuration contents;
- API and MCP credentials;
- operational logs, metrics, and inventory returned by tools;
- model requests, retrieved context, proposals, and diffs;
- approval capability IDs;
- audit records and actor attribution;
- host filesystem and process execution authority.

## Trust Boundaries

```text
network client
    | HTTP authentication and input limits
    v
Akritas host ---- OpenAI-compatible model (untrusted output)
    |  |  |
    |  |  +---- MCP stdio server (operator-configured, data untrusted)
    |  +------- RAG index (documents untrusted)
    +---------- workspace/temp copy/validators (repository code untrusted)
    |
    +---------- audit store (integrity-sensitive)
```

## Threats and Controls

| Threat | Existing or required control | Residual risk |
|---|---|---|
| Unauthenticated API use | Loopback default, Bearer authentication, reverse proxy guidance | Instance-wide shared token |
| Prompt injection from documents or alerts | Inputs labelled untrusted, operator-controlled global system instructions, runbook text grants no capability, host-side policy and execution evidence | Model can still select an irrelevant but authorized read-only query or give misleading advice |
| System-instructions tampering | Operator-selected bounded regular file, deployment filesystem permissions, read-only container image, startup-time loading | An actor with configuration write access can alter model behavior; host capability checks still apply |
| MCP privilege escalation | Explicit allowlist, pessimistic permissions, Web read-only filtering, host validation of every planned tool and argument | Authorized MCP process retains its OS permissions |
| VictoriaMetrics scope escape | Operator-fixed URL, tenant headers, credentials, read-only endpoints, bounded responses | MetricsQL may still select every series visible to the configured VictoriaMetrics identity |
| Arbitrary file read | Fixed RAG index, canonical workspace paths, traversal and symlink checks | Authorized workspace files remain visible to change preparation |
| Malicious structured proposal | Forced function, strict JSON, limits, exact replacements, host-generated diff | Semantically harmful but syntactically valid changes require human review |
| Approval theft or replay | Random bounded-lifetime ID, authentication, one-time consumption | Shared API token provides no per-user attribution |
| TOCTOU during Apply | Byte recheck, target-absence recheck, no-clobber create | Multi-file Apply is not crash-atomic |
| Secret leakage | Discovery exclusions, validation-copy exclusions, environment sanitization, bounded logs, disabled-by-default VictoriaMetrics error-body previews | Explicitly selected files and debug error bodies can contain sensitive content |
| Validator code execution | Opt-in profiles, fixed commands, offline Go settings, timeout, temp copy | Not an OS sandbox; direct network access remains possible |
| Resource exhaustion | Host-owned Run budgets for duration, model calls, planned and adaptive tool calls, context, model tokens, and aggregate results; one generation slot; bounded MCP HTTP duration and response size; bounded pending approvals | Expensive metrics queries and other authorized requests can still consume resources up to configured backend and host limits |
| Fabricated investigation evidence | Strict result schema and allowlisted host-created evidence references | A valid reference does not guarantee that the model interpreted the evidence correctly |
| Audit deletion or modification | Durable append-only application writes, restrictive volume permissions, backups | Host administrator can alter files; no signed log chain yet |
| Compromised upstream model | No direct filesystem or production access; host validates every action | Model can provide deceptive advice or repeatedly invalid proposals |

## Security Test Matrix

Critical tests must cover authentication failure, policy denial, malicious MCP
annotations and schemas, path and symlink escape, proposal limits, approval
expiry and replay, stale-source and create races, secret/environment redaction,
validator timeouts, audit persistence and corruption, and container non-root
operation. Test names should describe the threat they enforce.

## Review Triggers

Revisit this threat model whenever Akritas changes global-instruction loading,
adds write-capable tools, per-user
authorization, remote workspaces, new validator profiles, network-accessible MCP
transports, signed audit records, or multi-node deployment.
