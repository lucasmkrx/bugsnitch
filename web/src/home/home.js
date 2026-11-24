// SPDX-License-Identifier: MPL-2.0
// Preserve workspace links created before the public homepage was added.
if (location.hash.startsWith("#/")) {
  location.replace("/app/" + location.search + location.hash);
}

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
