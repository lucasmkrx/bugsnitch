# 0001 · Separate hosted coordination and local execution

Use a single Go service and PostgreSQL transactions for authoritative desired state, event history, idempotency and integration intents. Each runner owns a durable SQLite journal and isolated Git worktrees. Device snapshots carry bounded execution metadata. Transport never accepts shell commands or arbitrary paths from the service.

This permits useful coordination without transferring model credentials or assuming a hosted service is eligible for local plan authorization. Direct local execution remains explicitly initiated on the computer. Integration writes require separate authority and attributable evidence.
