# Security Policy

## Supported Versions

Akritas is under active development. Security fixes are applied to the current
mainline version only until tagged releases and a formal support window exist.

## Reporting a Vulnerability

Do not open a public issue for a suspected vulnerability. Send a private report
to the repository owner or the security contact configured by the deploying
organization. Include:

- the affected revision and deployment mode;
- a minimal reproduction;
- the expected and observed security boundary;
- potential impact and required attacker access;
- any suggested mitigation.

Do not include production credentials, private repository content, or customer
data. Operators should define a monitored security contact before exposing an
Akritas instance outside a local development environment.

## Operational Baseline

- Keep the default loopback bind unless a trusted TLS/SSO reverse proxy protects
  the service.
- Set `AKRITAS_API_KEY` and rotate it through the deployment secret mechanism.
- Mount RAG indexes and ordinary workspaces read-only.
- Grant a writable workspace only when Apply is required.
- Treat MCP servers, indexed documents, repositories, Alertmanager payloads, and
  model output as untrusted.
- Enable `go-test` only inside a separate container or VM security boundary.
- Keep write and dangerous MCP tools unavailable to the Web runtime.
- Protect audit storage from modification by the service's ordinary users.

## Security Guarantees and Non-Goals

Akritas validates bounded inputs, applies an explicit tool policy, isolates
change preparation in a temporary copy, and requires one-time approval before a
workspace write. These controls do not turn model-generated code or project
tests into safe programs. The temporary workspace and sanitized environment are
not an operating-system sandbox.

See [Security Model](docs/SECURITY_MODEL.md) and
[Threat Model](docs/THREAT_MODEL.md) for trust boundaries and residual risks.
