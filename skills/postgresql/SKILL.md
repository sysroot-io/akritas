---
name: postgresql
description: Read-only PostgreSQL investigation guidance for activity, autovacuum, waits, locks, connections, and growth.
match:
  - postgres
  - postgresql
---
# PostgreSQL Investigation Skill

Use this guidance when alert context or operational evidence makes PostgreSQL relevant to the investigation. Loading this skill alone does not establish that PostgreSQL is installed, affected, or responsible for the alert.

After a process check identifies PostgreSQL as a significant resource consumer, useful next checks commonly include:

- active queries and their duration;
- autovacuum workers and the relations they process;
- lock and wait events;
- connection pressure;
- table or index growth when storage is involved.

Call only tools present in the host-provided authorized tool catalog. Prefer specific PostgreSQL read-only tools such as `postgres.activity`, `postgres.autovacuum`, and `postgres.waits` when those exact capabilities are available. The skill does not create those capabilities and does not authorize writes, cancellation, termination, vacuum commands, configuration changes, or restarts.

Treat a failed or unavailable PostgreSQL check as a capability or execution gap. Do not present it as evidence that the corresponding cause was ruled out.
