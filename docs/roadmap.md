# Roadmap

No dates are promised. The local open-source core remains useful independently of any future hosted product.

## Phase 1 — Foundation

Implemented: repository detection, bounded recent history, commit inspection with changed files and patch previews, exact-line blame, and Snitch on Current Line. Remaining foundation work: extension-host automation, broader platform verification, release packaging and official publisher configuration.

## Phase 2 — Visual Git

- Proper commit graph with lanes (the graph model exists; lane visualization does not).
- Branches and tags as navigable history entry points.
- Commit comparison and richer diffs.
- File history and function/symbol history.

## Phase 3 — Git Forensics

- Investigation sessions and good/bad regression ranges.
- Visual Git bisect with careful confirmation for working-tree changes.
- Related-change analysis and suspicious commit ranking with explainable evidence.
- “What changed around this code?”
- Function-level provenance where technically feasible.

## Phase 4 — Integrations

Potential optional additions: GitHub/GitLab context, pull-request and issue correlation, and CI failure context. Network access must be explicit and separate from local inspection.

## Phase 5 — Optional intelligence

Potential additions include AI-assisted explanations or hosted analysis. These are not implemented, and the open-source local core must remain valuable without them. Hosted infrastructure or team workflows may live in a future separate private service; this repository does not implement billing or commercial services.
