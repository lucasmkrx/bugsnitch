// SPDX-License-Identifier: MPL-2.0
import { useEffect, useRef, useState } from "react";
import { post } from "./api";
import { Badge, Field, Panel } from "./ui";

const pairedRunner = "http://127.0.0.1:43182/";
const hexToken = /^[a-f0-9]{64}$/;

type Action = "status" | "models" | "save" | "remove";
type Grant = { deviceId: string; expiry: number; proof: string };
type Connection = {
  popup: Window;
  origin: string;
  manualToken: string;
  token?: string;
  permit?: Grant;
};
type Reply = {
  model?: string;
  models?: string[];
  error?: string;
  unavailable?: boolean;
};

function validGrant(value: unknown): value is Grant {
  if (!value || typeof value !== "object") return false;
  const grant = value as Partial<Grant>;
  return (
    typeof grant.deviceId === "string" &&
    /^[a-f0-9]{32}$/.test(grant.deviceId) &&
    Number.isSafeInteger(grant.expiry) &&
    typeof grant.proof === "string" &&
    hexToken.test(grant.proof)
  );
}

async function authorize(token: string, claim: Grant) {
  const hash = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(token),
  );
  const digest = Array.from(new Uint8Array(hash), (byte) =>
    byte.toString(16).padStart(2, "0"),
  ).join("");
  const { permit } = await post<{ permit: Grant }>("model-settings/authorize", {
    digest,
    claim: {
      deviceId: claim.deviceId,
      expiry: claim.expiry,
      proof: claim.proof,
    },
  });
  if (
    !validGrant(permit) ||
    permit.deviceId !== claim.deviceId ||
    permit.expiry !== claim.expiry
  )
    throw new Error("Invalid runner authorization");
  return permit;
}

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

  function disconnect(message = "", error = false) {
    clearConnection();
    setConnected(false);
    setBusy(false);
    setSavedModel("");
    setReadFailed(false);
    setModels([]);
    setFeedback(message);
    setFailed(error);
    if (key.current) key.current.value = "";
  }

  function request(action: Action, apiKey = "", selection = "") {
    const runner = connection.current;
    if (!runner?.token || runner.popup.closed) {
      disconnect("Reconnect to your runner to make changes.", true);
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
        permit: runner.permit,
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
      24_000,
    );
  }

  useEffect(() => {
    async function receive(event: MessageEvent) {
      const runner = connection.current;
      const data = event.data;
      if (
        !runner ||
        runner.popup !== event.source ||
        runner.origin !== event.origin
      )
        return;
      if (
        data?.type === "bugsnitch:model-settings-unavailable" &&
        !runner.token
      ) {
        disconnect("Start the paired Bugsnitch runner and try again.", true);
        return;
      }
      if (data?.type === "bugsnitch:model-settings-ready" && !runner.token) {
        const token = data.token;
        if (typeof token !== "string" || !hexToken.test(token)) return;
        try {
          let permit;
          // Verify a fixed-port listener with the workspace before sending credentials.
          if (runner.manualToken) {
            if (token !== runner.manualToken) return;
          } else {
            if (!validGrant(data.claim)) return;
            permit = await authorize(token, data.claim);
          }
          if (connection.current !== runner || runner.token) return;
          window.clearTimeout(timer.current);
          runner.token = token;
          runner.permit = permit;
          request("status");
        } catch {
          if (connection.current === runner)
            disconnect(
              "Could not verify this runner in your workspace. Update the runner or connect with a runner URL.",
              true,
            );
        }
        return;
      }
      const operation = pending.current;
      if (
        data?.type !== "bugsnitch:model-settings-reply" ||
        !runner.token ||
        runner.token !== data.token ||
        !operation ||
        data.id !== operation.id
      )
        return;
      window.clearTimeout(timer.current);
      pending.current = null;
      setBusy(false);
      const reply = data.reply as Reply;
      if (reply.unavailable) {
        disconnect(
          "The runner connection expired. Reconnect to continue.",
          true,
        );
        return;
      }
      setConnected(true);
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
            ? "Models loaded. Choose one that supports structured output."
            : "No models were returned. You can enter a model ID.",
        );
        return;
      }
      setSavedModel(reply.model || "");
      setReadFailed(false);
      setModel((current) =>
        operation.action === "status"
          ? current || reply.model || ""
          : reply.model || "",
      );
      if (operation.action === "save" || operation.action === "remove") {
        if (key.current) key.current.value = "";
        setModels([]);
        setFeedback(
          operation.action === "save"
            ? "OpenAI API key saved on this runner."
            : "OpenAI API key removed from this runner.",
        );
      }
    }
    window.addEventListener("message", receive);
    return () => {
      window.removeEventListener("message", receive);
      clearConnection();
    };
  }, []);

  function connect(url: URL) {
    const popup = window.open(url.href, "_blank", "popup,width=440,height=260");
    if (!popup) {
      setFailed(true);
      setFeedback("Allow pop-ups for Bugsnitch and try again.");
      return;
    }
    connection.current = {
      popup,
      origin: url.origin,
      manualToken: url.hash.slice(1),
    };
    setAddress("");
    setBusy(true);
    setFeedback("Connecting to your runner…");
    setFailed(false);
    timer.current = window.setTimeout(
      () =>
        disconnect(
          "Could not connect. Start the paired runner or connect with a runner URL.",
          true,
        ),
      18_000,
    );
  }

  function connectManually() {
    const text = address.trim();
    if (!/^http:\/\/127\.0\.0\.1:[1-9]\d{0,4}\/#([a-f0-9]{64})$/.test(text)) {
      setFailed(true);
      setFeedback("Paste the runner connection URL printed in your terminal.");
      return;
    }
    try {
      connect(new URL(text));
    } catch {
      setFailed(true);
      setFeedback("Paste the runner connection URL printed in your terminal.");
    }
  }

  return (
    <Panel
      title="API keys"
      action={connected ? <Badge>Runner connected</Badge> : undefined}
    >
      <form
        className="panel-body"
        onSubmit={(event) => {
          event.preventDefault();
          request("save", key.current?.value.trim(), model.trim());
        }}
      >
        <p>Your OpenAI key is saved on the runner on this computer.</p>
        <Field title="OpenAI API key">
          <input
            ref={key}
            type="password"
            autoComplete="off"
            spellCheck={false}
            maxLength={512}
            required={!savedModel}
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
            placeholder="Enter model ID"
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
          {connected ? (
            <>
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
                onClick={() => disconnect()}
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
                Save API key
              </button>
            </>
          ) : (
            <button
              type="button"
              className="primary"
              disabled={busy}
              onClick={() => connect(new URL(pairedRunner))}
            >
              Connect runner
            </button>
          )}
        </div>
        {feedback && <p role={failed ? "alert" : "status"}>{feedback}</p>}
      </form>
      {!connected && (
        <details className="panel-body">
          <summary>Connect with a runner URL</summary>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              connectManually();
            }}
          >
            <p>
              Run{" "}
              <code>{`bugsnitch model-settings --origin ${window.location.origin}`}</code>{" "}
              on the runner.
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
            <button disabled={busy || !address.trim()}>Connect with URL</button>
          </form>
        </details>
      )}
    </Panel>
  );
}
