# Threat Model

## Scope

This model covers the single-node Akritas service, its HTTP interfaces,
OpenAI-compatible upstream, MCP stdio children, local RAG index, operational
skill catalog, configured repository workspaces, validator processes, audit
storage, and configured outbound notification receivers.

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
    |  +------- skill catalog (operator-controlled guidance)
    +---------- workspace/temp copy/validators (repository code untrusted)
    |
    +---------- audit store (integrity-sensitive)
    |
    +---------- configured webhook / Telegram / Mattermost receiver
```

## Threats and Controls

| Threat | Existing or required control | Residual risk |
|---|---|---|
| Unauthenticated API use | Loopback default, Bearer authentication, reverse proxy guidance | Instance-wide shared token |
| Prompt injection from documents or alerts | Inputs labelled untrusted, operator-controlled global system instructions, runbook text grants no capability, host-side policy and execution evidence | Model can still select an irrelevant but authorized read-only query or give misleading advice |
| System-instructions tampering | Operator-selected bounded regular file, deployment filesystem permissions, read-only container image, startup-time loading | An actor with configuration write access can alter model behavior; host capability checks still apply |
| Service-configuration tampering | Strict versioned JSON, unknown-field rejection, bounded regular file, deployment filesystem permissions, secrets excluded from the schema | An actor with configuration write access can redirect trusted inputs or weaken deployment limits within host-accepted ranges |
| Skill over-disclosure or irrelevant skill loading | Exact automatic selectors, metadata-only `knowledge.list_skills`, exact-name `knowledge.load_skill`, bounded files, tool-call accounting, and one shared eight-skill Run cap | Deceptive inventory or model judgment can select irrelevant trusted guidance; loaded skill content reaches the upstream model |
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
| SSRF или утечка через исходящий webhook | URL и адресаты задаются только оператором в строгой bounded-конфигурации; alert и модель не управляют маршрутом; секреты остаются в environment | Компрометация конфигурации позволяет перенаправить операционные данные |
| Упоминания и Markdown-инъекция в чат | Адаптеры используют фиксированный шаблон, нейтрализуют `@`, схлопывают переносы в полях и экранируют Mattermost Markdown | Текст модели остаётся недоверенным содержимым сообщения |
| Повторный запуск расследования при сбое уведомления | Ошибка получателя возвращается как delivery status и audit event, но успешный Alertmanager Run остаётся HTTP 200 | Без внешнего outbox временный сбой может потерять уведомление |
| Подделка входящего bot message | Telegram polling использует bot token через TLS; webhooks используют отдельный provider secret и constant-time comparison; оба режима применяют conversation/user allowlists и bounded payload | Компрометация bot/provider secret и allowlisted account позволяет отправлять запросы в Chat loop |
| Telegram/Mattermost retry storm | Polling offset продвигается после queue acceptance; webhook update подтверждается до model execution; duplicate event IDs подавляются в bounded memory, full webhook queue отвечает 503 | Dedupe исчезает при restart; provider может повторить событие после restart |
| Bot feedback loop | Telegram messages с `is_bot` игнорируются; Mattermost inbound требует explicit user allowlist; model output нейтрализует mentions | Ошибочная allowlist, содержащая Mattermost bot user, может вернуть loop |
| Истощение памяти bot sessions | Bounded queue, history messages/bytes, session TTL, maximum session count, global generation slot | Allowlisted users могут занять очередь до configured bound |
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
transports, новый транспорт уведомлений, signed audit records, or multi-node deployment.
