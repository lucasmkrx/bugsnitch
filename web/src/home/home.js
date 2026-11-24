// SPDX-License-Identifier: MPL-2.0
// Preserve workspace links created before the public homepage was added.
if (location.hash.startsWith("#/")) {
  location.replace("/app/" + location.search + location.hash);
}

const stages = document.querySelector(".trace-tabs");
const tabs = Array.from(stages.querySelectorAll('[role="tab"]'));

function showStage(selected) {
  for (const tab of tabs) {
    const active = tab === selected;
    tab.setAttribute("aria-selected", String(active));
    tab.tabIndex = active ? 0 : -1;
    document.getElementById(tab.getAttribute("aria-controls")).inert = !active;
  }
}

tabs.forEach((tab, index) => {
  tab.addEventListener("click", () => showStage(tab));
  tab.addEventListener("keydown", (event) => {
    const next = {
      ArrowLeft: (index + tabs.length - 1) % tabs.length,
      ArrowRight: (index + 1) % tabs.length,
      Home: 0,
      End: tabs.length - 1,
    }[event.key];
    if (next === undefined) return;
    event.preventDefault();
    showStage(tabs[next]);
    tabs[next].focus();
  });
});
stages.hidden = false;

const copyButton = document.querySelector(".copy-button");
const command = document.querySelector("#checkout-command");
const status = document.querySelector("#copy-status");

if (navigator.clipboard?.writeText) {
  copyButton.hidden = false;
  copyButton.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(command.textContent);
      copyButton.textContent = "Copied";
      status.textContent = "Command copied to the clipboard.";
    } catch {
      const selection = window.getSelection();
      if (selection) {
        const range = document.createRange();
        range.selectNodeContents(command);
        selection.removeAllRanges();
        selection.addRange(range);
      }
      copyButton.textContent = "Copy command";
      status.textContent =
        "Use your browser’s copy shortcut to copy the selected command.";
    }
  });
}
