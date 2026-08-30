nftables:
  backend_allowlist:
    - name: orders-v1
      address: 10.20.4.10
      port: 8443
      sources:
        - frontend
