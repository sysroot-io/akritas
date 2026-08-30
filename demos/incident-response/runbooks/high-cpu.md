# High CPU on an API Node

Runbook reference: demo-high-cpu

1. Query CPU utilization for the affected instance over the last 30 minutes.
2. Check the top CPU-consuming processes using an authorized read-only tool.
3. Compare the alert start time with recent deployments.
4. If utilization remains above 95%, escalate to the platform on-call engineer.

Never restart a service automatically from this runbook. A restart is a write
action and requires a separate controlled workflow.
