# Reproducible performance evidence

Run `pnpm benchmark` from the root. The script builds its own Node runner, creates a fresh isolated 1,500-commit Git repository with fast-import, warms each operation once, measures 12 reads per operation and 50 graph layouts, writes ignored `.cache/benchmark.json`, and deletes its fixture. Inherited Git redirection and user identity/configuration are excluded during fixture creation. Setup is outside the timings.

The 7 October 2026 reference run used macOS x64, Intel i9-9980HK 2.40 GHz, Node 24.20.0, Git 2.39.5 (Apple Git-154), warm filesystem/process caches. These are local measurements, not service guarantees or results from a real production repository.

| Operation                                        | Median ms | Observed p95 ms | Samples |
| ------------------------------------------------ | --------: | --------------: | ------: |
| Repository snapshot                              |     47.82 |           53.18 |      12 |
| Frozen history page, 100 commits                 |     54.67 |           57.60 |      12 |
| Commit metadata + patch                          |     92.34 |           96.77 |      12 |
| Exact-line attribution + commit                  |    227.50 |          238.94 |      12 |
| File history, 100 commits                        |     57.88 |           59.06 |      12 |
| Ancestor range, 150 candidates                   |    155.91 |          160.23 |      12 |
| Graph, 5,000 linear commits                      |      1.64 |            5.89 |      50 |
| Graph, 5,000 commits / roughly 100 pending lanes |     39.69 |           61.42 |      50 |

Nearest-rank p95 is reported. Twelve samples make it approximately the largest observed read; use more runs and multiple machines for comparisons. The linear Git fixture has tiny one-line patches. Large trees, packed objects, antivirus, network-mounted filesystems, cold caches and concurrent applications can change timings substantially.

Graph layout has cost proportional to commit count and pending lane width. Wide histories allocate many per-row segments; the stress run's final process RSS was about 546 MiB, including repeated layouts and Node's retained heap. That is a process snapshot, not an extension-memory benchmark or isolated graph peak. Browser DOM rendering, native diff memory and startup are not included. Do not infer a 5,000-row interactive rendering guarantee from layout timings.

Keep default pages at 50, cap individual pages at 200, and expose bounded focus navigation rather than requiring thousands of loaded rows. Patch previews cap at 256 KiB, blob reads at 4 MiB, refs at 2,000, file history at 100 and ranges at 200. Package validation caps the compressed VSIX at 2 MiB. If real workload measurements justify improvement, profile lane lookup/segment allocation and browser row rendering separately before introducing indexing or virtualization.

The deterministic generated DAG regression checks 30 histories and every prefix, independently follows drawn ancestry, validates boundary reservations and preserves prior rows after pagination. It complements these timings; performance alone does not establish correctness.
