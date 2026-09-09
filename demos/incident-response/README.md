# Demo 1: Alert-to-Runbook Incident Response

This demo imports a real Markdown runbook, builds a BM25 index, starts Akritas,
and sends a valid Alertmanager v4 webhook. It demonstrates local context,
capability-gap reporting, the read-only tool boundary, and a durable run record.

```bash
go build -o bin/akritas ./cmd/akritas
./bin/akritas import-local \
  -input demos/incident-response/runbooks \
  -source demo-runbooks -license internal -validation-percent 0 \
  -output data/demo-runbooks
./bin/akritas build-rag-index \
  -manifest data/demo-runbooks/train/manifest.json \
  -output data/demo-runbooks.tgr

export AKRITAS_API_KEY=demo-only-change-me
./bin/akritas serve \
  -base-url http://127.0.0.1:8080/v1 \
  -rag-index data/demo-runbooks.tgr \
  -audit-log data/audit/demo.jsonl
```

In another terminal:

```bash
response=$(curl --fail-with-body -X POST http://127.0.0.1:8090/api/v1/alertmanager/webhook \
  -H 'Authorization: Bearer demo-only-change-me' \
  -H 'Content-Type: application/json' \
  --data-binary @demos/incident-response/alert.json)
echo "$response" | jq .

job_id=$(echo "$response" | jq -r '.events[0].job_id')
curl --fail -H 'Authorization: Bearer demo-only-change-me' \
  "http://127.0.0.1:8090/api/v1/investigation-jobs/${job_id}"
```

The webhook returns HTTP 202 after durable acceptance. Poll the job endpoint
until it reports `succeeded`, then open its `run_id` at `/runs/{id}` or
`GET /api/v1/runs/{id}`. Expected evidence: the Run references document ID
`high-cpu.md`, unavailable diagnostics are listed as capability gaps, no write
tool is exposed, and the Run source is `alert:alertmanager`. Results depend on
the configured upstream model; the host-side safety assertions do not.
