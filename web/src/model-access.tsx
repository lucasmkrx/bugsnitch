// SPDX-License-Identifier: MPL-2.0
import { useEffect, useRef, useState } from "react";
import { Badge, Field, Panel } from "./ui";

type Action = "status" | "models" | "save" | "remove";
type Connection = { popup: Window; origin: string; token: string };
type Reply = { model?: string; models?: string[]; error?: string };

export function ModelAccess() {
  const [address, setAddress] = useState("");
  const [connected, setConnected] = useState(false);
  const [busy, setBusy] = useState(false);
  const [savedModel, setSavedModel] = useState("");
  const [readFailed, setReadFailed] = useState(false);
  const [model, setModel] = useState("");
  const [models, setModels] = useState<string[]>([]);
  const [feedback, setFeedback] = useState("");
  const [failed, setFailed] = useState(false);
  const key = useRef<HTMLInputElement>(null);
  const connection = useRef<Connection | null>(null);
  const pending = useRef<{ id: number; action: Action } | null>(null);
  const sequence = useRef(0);
  const timer = useRef<number>(0);

  function clearConnection() {
    window.clearTimeout(timer.current);
    connection.current?.popup.close();
    connection.current = null;
    pending.current = null;
  }

  function disconnect(message: string, error = false) {
    clearConnection();
    setConnected(false);
    setBusy(false);
    setSavedModel("");
    setReadFailed(false);
    setModel("");
    setModels([]);
    setFeedback(message);
    setFailed(error);
    if (key.current) key.current.value = "";
  }

  function request(action: Action, apiKey = "", selection = "") {
    const runner = connection.current;
    if (!runner || runner.popup.closed) {
      disconnect("Start a new runner connection to make changes.", true);
      return;
    }
    if (pending.current) return;
    const id = ++sequence.current;
    pending.current = { id, action };
    setBusy(true);
    setFeedback("");
    setFailed(false);
    runner.popup.postMessage(
      {
        type: "bugsnitch:model-settings-request",
        token: runner.token,
        id,
        action,
        apiKey,
        model: selection,
      },
      runner.origin,
    );
    timer.current = window.setTimeout(
      () =>
        disconnect(
          "The runner did not respond. Reconnect to check your settings.",
          true,
        ),
      14_000,
    );
  }

  useEffect(() => {
    function receive(event: MessageEvent) {
      const runner = connection.current;
      const data = event.data;
      if (
        !runner ||
        runner.popup !== event.source ||
        runner.origin !== event.origin ||
        runner.token !== data?.token
      )
        return;
      if (data.type === "bugsnitch:model-settings-ready" && !pending.current) {
        window.clearTimeout(timer.current);
        setConnected(true);
        request("status");
        return;
      }
      const operation = pending.current;
      if (
        data.type !== "bugsnitch:model-settings-reply" ||
        !operation ||
        data.id !== operation.id
      )
        return;
      window.clearTimeout(timer.current);
      pending.current = null;
      setBusy(false);
      const reply = data.reply as Reply;
      if (reply.error) {
        if (operation.action === "status") setReadFailed(true);
        setFailed(true);
        setFeedback(reply.error);
        return;
      }
      if (operation.action === "models") {
        setModels(reply.models || []);
        setFeedback(
          reply.models?.length
            ? "Models loaded. Choose one that supports text and structured output."
            : "This key returned no model IDs. You can enter a model ID manually.",
        );
        return;
      }
      setSavedModel(reply.model || "");
      setReadFailed(false);
      setModel(reply.model || "");
      if (operation.action === "save" || operation.action === "remove") {
        if (key.current) key.current.value = "";
        setModels([]);
        setFeedback(
          operation.action === "save"
            ? "OpenAI settings saved on this runner."
            : "OpenAI settings removed from this runner.",
        );
      }
    }
    window.addEventListener("message", receive);
    return () => {
      window.removeEventListener("message", receive);
      clearConnection();
    };
  }, []);

  function connect() {
    const text = address.trim();
    if (!/^http:\/\/127\.0\.0\.1:[1-9]\d{0,4}\/#([a-f0-9]{64})$/.test(text)) {
      setFailed(true);
      setFeedback("Paste the runner connection URL printed in your terminal.");
      return;
    }
    let url: URL;
    try {
      url = new URL(text);
    } catch {
      setFailed(true);
      setFeedback("Paste the runner connection URL printed in your terminal.");
      return;
    }
    const popup = window.open(url.href, "_blank", "popup,width=440,height=260");
    if (!popup) {
      setFailed(true);
      setFeedback("Allow pop-ups for this dashboard and try again.");
      return;
    }
    connection.current = {
      popup,
      origin: url.origin,
      token: url.hash.slice(1),
    };
    setAddress("");
    setBusy(true);
    setFeedback("Connecting to your runner…");
    setFailed(false);
    timer.current = window.setTimeout(
      () =>
        disconnect(
          "Could not connect. Check that the terminal is still running and the dashboard origin matches.",
          true,
        ),
      8_000,
    );
  }

  return (
    <Panel
      title="Model access"
      action={connected ? <Badge>Local runner connected</Badge> : undefined}
    >
      {connected ? (
        <form
          className="panel-body"
          onSubmit={(event) => {
            event.preventDefault();
            request("save", key.current?.value.trim(), model.trim());
          }}
        >
          <p>
            Your key is saved in your local runner profile. The selected model
            is used for OpenAI turns on this runner.
          </p>
          <Field title="OpenAI API key">
            <input
              ref={key}
              type="password"
              autoComplete="off"
              spellCheck={false}
              maxLength={512}
              disabled={busy}
              placeholder={
                savedModel ? "Leave blank to keep your saved key" : undefined
              }
            />
          </Field>
          <Field title="OpenAI model">
            <input
              value={model}
              onChange={(event) => setModel(event.target.value)}
              list="openai-models"
              maxLength={120}
              autoComplete="off"
              spellCheck={false}
              placeholder="Enter model ID or load models"
              required
              disabled={busy}
            />
          </Field>
          <datalist id="openai-models">
            {models.map((id) => (
              <option key={id} value={id} />
            ))}
          </datalist>
          <div className="form-footer">
            <button
              type="button"
              disabled={busy}
              onClick={() => request("models", key.current?.value.trim())}
            >
              Load models
            </button>
            <button
              type="button"
              disabled={busy}
              onClick={() => disconnect("")}
            >
              Disconnect
            </button>
            {(savedModel || readFailed) && (
              <button
                type="button"
                disabled={busy}
                onClick={() => request("remove")}
              >
                Remove saved key
              </button>
            )}
            <button className="primary" disabled={busy || !model.trim()}>
              Save model settings
            </button>
          </div>
          {feedback && <p role={failed ? "alert" : "status"}>{feedback}</p>}
        </form>
      ) : (
        <form
          className="panel-body"
          onSubmit={(event) => {
            event.preventDefault();
            connect();
          }}
        >
          <p>
            Connect to your runner from the browser on the same computer to use
            your own OpenAI API key and model.
          </p>
          <p>
            Run{" "}
            <code>{`bugsnitch model-settings --origin ${window.location.origin}`}</code>{" "}
            in your runner’s terminal.
          </p>
          <Field title="Runner connection URL">
            <input
              type="password"
              value={address}
              onChange={(event) => setAddress(event.target.value)}
              autoComplete="off"
              spellCheck={false}
              required
              disabled={busy}
            />
          </Field>
          <button className="primary" disabled={busy || !address.trim()}>
            Connect runner
          </button>
          {feedback && <p role={failed ? "alert" : "status"}>{feedback}</p>}
        </form>
      )}
    </Panel>
  );
}
