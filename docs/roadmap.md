# Roadmap

No dates are promised. The local open-source core remains useful independently of any future hosted product.

## Phase 1 — Foundation

Implemented: repository detection, bounded recent history, commit inspection with changed files and patch previews, exact-line blame, Snitch on Current Line, automated VS Code host checks across Linux/macOS/Windows, and GitHub preview packaging. Remaining release work: official Marketplace publisher configuration and broader user feedback.

## Phase 2 — Visual Git

The focused 0.2.0 milestone:

- Stable ancestry lanes alongside history, including branches, merges, and page continuations (implemented).
- Connect line provenance, commit inspection, and graph selection without losing investigation context (implemented).
- Browse branches and tags without checking them out or changing the working tree (implemented).

A commit attributed to a line is an investigation lead, not proof that it introduced a regression. This milestone adds no mutating Git operations, AI, billing, or cloud services.

Later visual Git work:

- Historical per-file and arbitrary merge-parent comparisons (implemented in 0.3.0); richer diff presentation remains future work.
- Rename-following file history (implemented in 0.3.0); function/symbol history remains future work.

## Phase 3 — Git Forensics

- In-memory investigation sessions, ancestor-validated good/bad ranges, manual notes and explicit export (implemented in 0.3.0). Persistent/importable sessions remain future work.
- Visual Git bisect with careful confirmation for working-tree changes.
- Related-change analysis and suspicious commit ranking with explainable evidence.
- “What changed around this code?”
- Function-level provenance where technically feasible.

## Phase 4 — Integrations

Potential optional additions: GitHub/GitLab context, pull-request and issue correlation, and CI failure context. Network access must be explicit and separate from local inspection.

## Phase 5 — Optional intelligence

Potential additions include AI-assisted explanations or hosted analysis. These are not implemented, and the open-source local core must remain valuable without them. Hosted infrastructure or team workflows may live in a future separate private service; this repository does not implement billing or commercial services.
