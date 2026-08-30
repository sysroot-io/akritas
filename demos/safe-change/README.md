# Demo 2: Validated Change with One-Time Approval

This demo uses the included nftables fixture to exercise workspace catalog
selection, structured exact replacements, a temporary workspace, validators,
host-generated diff, one-time approval, and stale-source protection.

Start an OpenAI-compatible model endpoint, then run:

```bash
go build -o bin/akritas ./cmd/akritas
export AKRITAS_API_KEY=demo-only-change-me
./bin/akritas serve \
  -base-url http://127.0.0.1:8080/v1 \
  -workspace nftables=testdata/ops-change/nftables \
  -audit-log data/audit/demo.jsonl
```

Open `http://127.0.0.1:8090`, select **Prepare change**, choose `nftables`, and
submit the request from `testdata/ops-change/nftables/REQUEST.md`. Review the
structured proposal, validator results, and host-generated diff before clicking
the approval button.

Expected evidence: preview does not alter the fixture; apply requires explicit
confirmation; the approval cannot be reused; modifying a source file between
preview and apply causes a conflict; `/api/v1/runs` contains separate
`change_preview` and `change_apply` records. Restore the fixture through version
control before repeating the successful apply path.

