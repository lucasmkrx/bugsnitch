// SPDX-License-Identifier: MPL-2.0
(async () => {
  const dashboard = document.body.dataset.dashboard;
  const opener = window.opener;
  const status = document.getElementById("status");
  const paired = document.body.dataset.paired === "true";
  let token = location.hash.slice(1);
  let claim;
  history.replaceState(null, "", "/");
  if (!opener || (!paired && !/^[a-f0-9]{64}$/.test(token))) {
    status.textContent = "Open this connection from API keys in Bugsnitch Settings.";
    return;
  }
  if (paired) {
    try {
      const response = await fetch("/connect", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: "{}",
        cache: "no-store",
        signal: AbortSignal.timeout(12000),
      });
      if (!response.ok) throw new Error();
      ({ token, claim } = await response.json());
    } catch {
      status.textContent = "The paired runner could not verify its connection.";
      opener.postMessage({ type: "bugsnitch:model-settings-unavailable" }, dashboard);
      return;
    }
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
      (paired && !request.permit) ||
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
          permit: paired ? request.permit : undefined,
        }),
        cache: "no-store",
        signal: AbortSignal.timeout(22000),
      });
      if (!response.ok) throw new Error();
      reply = await response.json();
    } catch {
      reply = { unavailable: true, error: "The runner connection expired. Reconnect to continue." };
    } finally {
      busy = false;
    }
    opener.postMessage(
      { type: "bugsnitch:model-settings-reply", token, id: request.id, reply },
      dashboard,
    );
  });
  status.textContent = "Return to Bugsnitch Settings to save your API key.";
  opener.postMessage(
    { type: "bugsnitch:model-settings-ready", token, claim },
    dashboard,
  );
})();
