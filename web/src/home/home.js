// SPDX-License-Identifier: MPL-2.0
// Preserve workspace links created before the public homepage was added.
if (location.hash.startsWith("#/")) {
  location.replace("/app/" + location.search + location.hash);
}

const walkthrough = document.querySelector(".walkthrough");
const viewport = walkthrough.querySelector(".walkthrough-viewport");
const steps = [...walkthrough.querySelectorAll(".run-progress li")];
const scenes = [...walkthrough.querySelectorAll(".walkthrough-scene")];
const canPin = matchMedia(
  "(min-height: 520px) and (prefers-reduced-motion: no-preference)",
);
const clamp = (value) => Math.max(0, Math.min(1, value));
let isPinned;
let current = -1;
let frame;

function measureTrack() {
  return {
    offset: walkthrough.getBoundingClientRect().top,
    distance: walkthrough.offsetHeight - viewport.offsetHeight,
  };
}

function setCurrent(index) {
  if (current === index) return;
  current = index;
  for (const [stepIndex, step] of steps.entries()) {
    const active = stepIndex === current;
    scenes[stepIndex].inert = isPinned && !active;
    if (active) step.setAttribute("aria-current", "step");
    else step.removeAttribute("aria-current");
  }
}

function update() {
  frame = undefined;
  if (!isPinned) return;
  const { offset, distance } = measureTrack();
  const progress = clamp(-offset / Math.max(distance, 1));
  const position = progress * steps.length;
  const index = Math.min(Math.floor(position), steps.length - 1);
  const sceneProgress = position - index;
  const slideStart = 0.68;
  const phase =
    index < steps.length - 1
      ? clamp((sceneProgress - slideStart) / (1 - slideStart))
      : 0;
  // Ease the handoff as a function of scroll, including when scrolling back.
  const slide = phase * phase * (3 - 2 * phase);
  setCurrent(index + Number(slide >= 0.5));
  viewport.style.setProperty("--scroll-progress", progress);

  for (const [sceneIndex, scene] of scenes.entries()) {
    const outgoing = sceneIndex === index;
    const entering = sceneIndex === index + 1 && slide > 0;
    const localProgress = outgoing ? clamp(sceneProgress / slideStart) : 0;
    const x = outgoing ? -slide : 1 - slide;
    scene.style.setProperty("--slide-offset", `${x * 100}%`);
    scene.style.setProperty(
      "--scene-visibility",
      outgoing || entering ? "visible" : "hidden",
    );
    scene.style.setProperty("--scene-shift", `${7 - localProgress * 14}px`);
    scene.style.setProperty("--scene-zoom", 1 + localProgress * 0.012);
    scene.style.setProperty("--scene-progress", localProgress);
  }
}

function scheduleUpdate() {
  if (frame === undefined) frame = requestAnimationFrame(update);
}

function layout() {
  walkthrough.toggleAttribute("data-pinned", canPin.matches);
  isPinned = canPin.matches && viewport.offsetHeight <= innerHeight + 1;
  walkthrough.toggleAttribute("data-pinned", isPinned);
  current = -1;
  if (!isPinned) setCurrent(0);
  scheduleUpdate();
}

walkthrough.style.setProperty("--step-count", steps.length);
canPin.addEventListener("change", layout);
window.addEventListener("scroll", scheduleUpdate, { passive: true });
window.addEventListener("resize", layout);
window.addEventListener("pageshow", layout);
layout();
document.fonts.ready.then(layout);

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
