# Incoming Alert Contract

## Purpose

Akritas converts monitoring-provider payloads into one versioned contract before
deduplication, correlation, investigation, or notification. Downstream code does
not depend on Alertmanager, Uptime Kuma, Pingdom, or another provider's field
names.

## Ingress

Configured sources use:

```text
POST /api/v1/alerts/{source}/webhook
```

`{source}` selects a fixed adapter and authentication policy from
`alert_sources_config`. It is a configuration identifier, not a model-selected
value. The legacy `POST /api/v1/alertmanager/webhook` route remains an
authenticated compatibility alias.

Requests are limited to 1 MiB and a batch can contain at most 128 events.
Provider data is always treated as untrusted content.

## Canonical Event Version 1

```json
{
  "schema_version": 1,
  "event_id": "evt_host_generated",
  "source": {
    "type": "alertmanager",
    "name": "alertmanager-main",
    "provider_event_id": "8f6c2b15a4d91e20"
  },
  "state": "firing",
  "name": "HighCPU",
  "severity": "critical",
  "summary": "CPU usage is above 95%",
  "description": "pg01 CPU has exceeded 95% for 10 minutes",
  "entity": {
    "kind": "instance",
    "id": "pg01",
    "display_name": "pg01"
  },
  "related_entities": [],
  "started_at": "2026-09-04T08:00:00Z",
  "observed_at": "2026-09-04T08:01:00Z",
  "labels": {"role": "postgresql"},
  "annotations": {},
  "links": [{"type": "generator", "url": "https://prometheus.example/graph"}],
  "deduplication_key": "provider-or-derived-key",
  "correlation_key": "provider-or-derived-key"
}
```

Fields:

- `event_id` is assigned by Akritas after acceptance. A generic client leaves it
  empty or omits it.
- `source.type` is the configured adapter type; `source.name` is the configured
  source identifier. Clients cannot override either value.
- `provider_event_id` is the provider's stable alert/check identity when one is
  available.
- `state` is `firing` or `resolved`.
- `severity` is `unknown`, `info`, `warning`, or `critical`.
- `entity` is the primary host, service, pod, monitor, check, or other affected
  object. `related_entities` is optional.
- timestamps are RFC 3339 and normalized to UTC. `ended_at` is optional.
- label and annotation keys are normalized to lowercase and collections are
  bounded.
- links must use absolute HTTP(S) URLs.
- missing deduplication and correlation keys are deterministically derived from
  stable source, entity, state, identity, and timestamp fields.

The generic adapter accepts a batch object with `schema_version: 1` and an
`alerts` array containing these events. Source identity and host-generated IDs
may be omitted.

## Provider Mapping

| Adapter | Provider identity | Primary entity | State mapping |
|---|---|---|---|
| `alertmanager` | alert fingerprint, then group key | instance, host, pod, service, job, then alert | firing/resolved |
| `uptime-kuma` | monitor ID | monitor | heartbeat 0 firing, 1 resolved |
| `pingdom` | check ID | check | DOWN/alert states firing, UP/recovered states resolved |
| `generic` | supplied or derived | supplied entity | canonical state |

Adapters preserve useful provider labels, annotations, timestamps, and safe
links but do not preserve the raw request body. The delivery journal stores only
its SHA-256 hash alongside canonical records.

## Authentication

Every configured source must define authentication or explicitly opt into
`allow_unauthenticated` for a trusted reverse-proxy deployment. Available
controls can be combined:

- `bearer_token_env` checks `Authorization: Bearer ...`;
- `hmac_secret_env` checks `X-Akritas-Signature: sha256=<hex>` over exact body
  bytes;
- `secret_header` plus `secret_env` checks an operator-selected header.

Comparisons are constant time. Secret values remain in environment variables;
the JSON file contains only their names.

## Durable Processing

One append-only JSONL transaction records the delivery, new canonical events,
incident updates, and pending jobs before the HTTP response is sent.

- Repeated deduplication keys for the same source reference the first event and
  do not create another job.
- A firing event opens an incident by source plus correlation key.
- Later firing events update that open incident without starting a second Run.
- A resolved event closes the open incident.
- A later firing event after resolution opens a new incident and job.

New events return HTTP `202 Accepted`. A delivery containing only duplicates
returns HTTP `200 OK`. Validation failures return `400`, source authentication
failures `401`, unknown source names `404`, and unavailable persistence `503`.

## Investigation Worker

The default worker runs inside the Akritas service process. It claims persisted
jobs, executes the normal bounded RAG/planning/skill/read-only-tool loop, stores
the structured result in a Run, and dispatches notifications. An interrupted
`running` job becomes `pending` during the next startup.

The job-store boundary is intentionally independent of the adapters and model
loop. A future external queue and separate worker binary can reuse the same
canonical event, incident, job, and API contracts.

## Query APIs

```text
GET /api/v1/incidents
GET /api/v1/incidents/{id}
GET /api/v1/alert-events/{id}
GET /api/v1/investigation-jobs/{id}
GET /api/v1/runs
GET /api/v1/runs/{id}
POST /api/v1/runs/{id}/chat
```

These routes use the common Akritas API authentication. Run list responses omit
raw tool payloads; Run detail includes host-recorded arguments and raw results.
Follow-up tool calls append to the selected Run timeline.
