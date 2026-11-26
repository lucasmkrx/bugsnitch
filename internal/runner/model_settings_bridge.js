// SPDX-License-Identifier: MPL-2.0
(() => {
  const dashboard = document.body.dataset.dashboard;
  const opener = window.opener;
  const token = location.hash.slice(1);
  history.replaceState(null, "", "/");
  if (!opener || !/^[a-f0-9]{64}$/.test(token)) {
    document.getElementById("status").textContent =
      "Open this connection from Model access in your Bugsnitch dashboard.";
    return;
  }
  let busy = false;
  window.addEventListener("message", async (event) => {
    const request = event.data;
    if (
      event.origin !== dashboard ||
      event.source !== opener ||
      request?.type !== "bugsnitch:model-settings-request" ||
      request.token !== token ||
      !Number.isSafeInteger(request.id) ||
      busy
    )
      return;
    busy = true;
    let reply;
    try {
      const response = await fetch("/settings", {
        method: "POST",
        headers: {
          Authorization: "Bearer " + token,
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          action: request.action,
          apiKey: request.apiKey,
          model: request.model,
        }),
        cache: "no-store",
        signal: AbortSignal.timeout(12000),
      });
      if (!response.ok) throw new Error();
      reply = await response.json();
    } catch {
      reply = { error: "The runner connection expired. Start a new session." };
    } finally {
      busy = false;
    }
    opener.postMessage(
      { type: "bugsnitch:model-settings-reply", token, id: request.id, reply },
      dashboard,
    );
  });
  opener.postMessage(
    { type: "bugsnitch:model-settings-ready", token },
    dashboard,
  );
})();
