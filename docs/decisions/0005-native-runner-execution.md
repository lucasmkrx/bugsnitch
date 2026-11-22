# 0005 · Rust owns local worktrees and validators

Accepted 2026-11-22. The runner needs the exact repository, revision and subprocess outcome to decide whether a task can advance or release capacity.

Reuse the packaged Rust companion and system Git. Give each operation a bounded private input and a defined result. Rust verifies the worktree's forward and reverse Git registrations. It preserves dirty work and archives a detached integration result before explicit cleanup. Git subprocesses finish or time out before the next stage proceeds.

Run each validator under a dedicated Rust supervisor. The Go owner keeps a control pipe open while the stage is authorized. Rust applies the validator timeout, stops the process group and confirms that its descendants have released their resources. Linux adopts and reaps orphaned group members. macOS verifies group membership while the leader’s PID is still reserved. A missing terminal receipt keeps the outcome uncertain. This design handles the Go owner's disappearance while the supervisor remains alive. A validator that deliberately detaches still requires separate OS-level containment.

Keep pause and stop receipt transitions and the final attempt version in one SQLite transaction. Existing command IDs and on-disk attempt records remain usable across upgrades. The Go entry point continues to handle hosted authority, issue checks, model turns and artifact assembly. [Runner recovery](../RUNNER.md#recovery-and-retention)
