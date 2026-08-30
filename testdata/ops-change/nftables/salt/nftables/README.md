# Backend Allowlist Management

The source of truth for the production backend allowlist is
`pillar/prod/nftables.sls`. Verify the address and port against
`inventory/backends.yaml`.

The `salt/nftables/templates/backends.nft.jinja` template is shared and must be
changed only when the schema changes. After changing the pillar, run:

```bash
yamllint pillar/prod/nftables.sls
salt-call --local state.show_sls nftables pillarenv=prod
nft -c -f rendered/nftables.conf
```
