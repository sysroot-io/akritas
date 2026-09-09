# Single-Node Deployment

## Supported Shape

The included container deployment runs one Akritas process and keeps its audit
log in a named Docker volume. The HTTP port is published on loopback only. An
OpenAI-compatible model endpoint runs separately and can be reached through
`host.docker.internal` or a private container network.

This is a single-node deployment, not a high-availability design. Back up the
audit volume and workspace repositories according to local retention policy.

## Start

```bash
cp .env.example .env
# Set a long random AKRITAS_API_KEY in .env.
docker compose up --build -d
docker compose ps
curl --fail http://127.0.0.1:8090/health
```

The container filesystem is read-only, all Linux capabilities are dropped, and
privilege escalation is disabled. Only `/tmp` and the named audit volume are
writable. The image includes the Go toolchain so configured Go validators can
run inside an explicitly mounted workspace. Its built-in
`/var/lib/akritas/configs/server.json` selects the container-safe listen
address, host upstream URL, and audit path; `AKRITAS_CONFIG` can select a
mounted replacement.

## Add RAG and Workspaces

The included Compose service runs only `akritas serve`; settings come from
`.env`. For a larger deployment, mount a service configuration and its inputs
through a local Compose override:

```yaml
services:
  akritas:
    environment:
      AKRITAS_CONFIG: /etc/akritas/server.json
      AKRITAS_NOTIFICATIONS_CONFIG: /etc/akritas/notifications.json
      AKRITAS_ALERT_SOURCES_CONFIG: /etc/akritas/alerts.json
      AKRITAS_PUBLIC_URL: https://akritas.example
    volumes:
      - ./configs/akritas/server.local.json:/etc/akritas/server.json:ro
      - ./configs/akritas/notifications.local.json:/etc/akritas/notifications.json:ro
      - ./configs/akritas/alerts.local.json:/etc/akritas/alerts.json:ro
      - ./data/indexes:/var/lib/akritas/indexes:ro
      - ./configs/akritas/workspaces.local.json:/etc/akritas/workspaces.json:ro
      - /srv/repos/backend:/workspaces/backend:rw
```

The mounted `server.local.json` should keep `address` at `0.0.0.0:8090` and can
set `rag_index` to `/var/lib/akritas/indexes/operations.tgr`,
`workspace_config` to `/etc/akritas/workspaces.json`, and `audit_log` to
`/var/lib/akritas/audit/akritas.jsonl` and `alert_store` to
`/var/lib/akritas/audit/alerts.jsonl`. Start from
`configs/akritas/server.example.json`. Environment variables can override any
field and explicit CLI flags remain the highest-precedence option.

Do not place outbound webhook, Telegram, or Mattermost secrets in mounted JSON.
The `*_env` fields in notification configuration must reference environment
variables supplied by the container runtime from a secret store. Restrict
egress to configured hosts. Built-in delivery is best effort without persistent
retry; route the generic webhook to a durable queue when delivery guarantees
are required.

Telegram polling needs only outbound HTTPS access to `api.telegram.org` and no
public ingress route. Ensure that no webhook is registered for the same bot
token. For Telegram webhook mode and Mattermost, the reverse proxy must allow
the ingress routes without injecting the common Akritas Bearer token:

```text
/api/v1/bots/telegram/*/webhook
/api/v1/bots/mattermost/*/webhook
```

Preserve the Telegram secret header and Mattermost request body, limit request
bodies to 64 KiB, enable rate limiting, and omit provider secrets from access
logs. These endpoints perform their own provider authentication and allowlist
checks. Other `/api/v1/*` routes must still require `AKRITAS_API_KEY` in Akritas
and, where applicable, at the reverse proxy.

Container workspace roots in the catalog must use their mounted paths, such as
`/workspaces/backend`. Use `:ro` for diagnostic-only workspaces. Change apply
requires `:rw`; preview still runs in an isolated temporary copy and one-time
approval plus stale-source verification remain mandatory.

## Operations

View logs with `docker compose logs -f akritas`. The audit records are stored
inside the `akritas-audit` volume at `akritas.jsonl`; use the authenticated
`GET /api/v1/runs` endpoint for normal inspection. Treat volume-level access as
privileged because metadata can contain internal hostnames and workspace names.

Upgrade with `docker compose build --pull` followed by
`docker compose up -d`. Stop without deleting audit data with
`docker compose down`. Do not use `docker compose down -v` unless permanent
audit deletion is intentional and covered by retention policy.
