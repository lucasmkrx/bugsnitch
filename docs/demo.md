# Try a two-minute investigation

This controlled example contains a real Git branch and merge with an intentionally changed calculation. It makes the investigation reproducible without claiming to represent a production incident.

Install the [preview VSIX](https://github.com/lucasmkrx/bugsnitch/releases) or [build from source](../README.md#run-from-source). With Node 24 and Git available, run from a clone of this repository:

```sh
node scripts/create-demo.mjs
```

The script prints the path to a fresh temporary repository. It creates commits, branches, and tags only in that new repository. Open the printed folder in VS Code.

1. Open `checkout.js`, put the cursor on **line 2**, and run **Bugsnitch: Snitch on Current Line**. It should identify **Simplify total calculation**.
2. Choose **Inspect this commit**. The patch removes `* item.quantity`; an item priced at 10 with quantity 3 now contributes 10 rather than 30.
3. Choose **Show in graph**. Explore the parent commit and the merge into `main`. Inspection gives evidence for the change; the explicit input/output example establishes the behavioral difference in this fixture.
4. Select the **known-good** tag as the history entry point. Its tip retains the quantity calculation. Select **feature/faster-totals** to inspect the change before its merge.
5. Return to **Current checkout (HEAD)**. Your checked-out branch and files still match the merged history. Browsing did not check out the tag or feature branch.

The fixture has 15 commits, so a page size of 10 also lets you exercise **Load more**. The **investigate** tag points at the merge. Delete the temporary folder when finished; the script does not install anything or configure your normal VS Code profile.

## Recording a product demo

Capture the actual VS Code window with this folder open. Show the editor command, associated commit, changed line, graph, and known-good tag in that order. A 30–60 second recording is enough. Use a theme with readable contrast, keep the cursor movements deliberate, and label the repository as demo data.

The README currently contains a production-webview screenshot. It does not yet contain a recording of the VS Code shell; the walkthrough above is reproducible today.
