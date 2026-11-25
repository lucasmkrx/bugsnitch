// SPDX-License-Identifier: MPL-2.0
import { useEffect, useRef, useState, type FormEvent } from "react";
import { messageOf, post, request } from "./api";
import { refresh } from "./store";
import { Field } from "./ui";

type Recaptcha = {
  ready(callback: () => void): void;
  execute(siteKey: string, options: { action: string }): Promise<string>;
};

declare global {
  interface Window {
    grecaptcha?: { enterprise?: Recaptcha };
  }
}

let enterpriseScript: Promise<Recaptcha> | undefined;

function loadRecaptcha(siteKey: string): Promise<Recaptcha> {
  enterpriseScript ??= new Promise<Recaptcha>((resolve, reject) => {
    const script = document.createElement("script");
    const fail = () => {
      clearTimeout(timer);
      script.remove();
      reject(new Error("reCAPTCHA could not load. Try again."));
    };
    const timer = window.setTimeout(fail, 10_000);
    script.onerror = fail;
    script.onload = () => {
      const enterprise = window.grecaptcha?.enterprise;
      if (!enterprise) {
        fail();
        return;
      }
      enterprise.ready(() => {
        clearTimeout(timer);
        resolve(enterprise);
      });
    };
    script.src =
      "https://www.google.com/recaptcha/enterprise.js?render=" +
      encodeURIComponent(siteKey);
    script.async = true;
    document.head.append(script);
  }).catch((error: unknown) => {
    enterpriseScript = undefined;
    throw error;
  });
  return enterpriseScript;
}

export function useEmailSignIn(enabled = true) {
  const [siteKey, setSiteKey] = useState<string | null>(null);
  useEffect(() => {
    if (!enabled) return;
    const abort = new AbortController();
    void request<{ siteKey: string | null }>("login/email", {
      signal: abort.signal,
    })
      .then(({ siteKey }) => {
        if (!abort.signal.aborted) setSiteKey(siteKey);
      })
      .catch(() => {});
    return () => abort.abort();
  }, [enabled]);
  return enabled ? siteKey : null;
}

export function EmailSignIn({
  siteKey,
  online,
}: {
  siteKey: string;
  online: boolean;
}) {
  const [email, setEmail] = useState("");
  const [challenge, setChallenge] = useState("");
  const [code, setCode] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const pending = useRef(false);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending.current || !online) return;
    pending.current = true;
    setBusy(true);
    setError("");
    try {
      if (challenge) {
        await post("login/email/verify", { challenge, code });
        await refresh({ afterWrite: true });
      } else {
        const captcha = await loadRecaptcha(siteKey);
        const captchaToken = await captcha.execute(siteKey, {
          action: "email_sign_in",
        });
        const result = await post<{ challenge: string }>("login/email/start", {
          email: email.trim(),
          captchaToken,
        });
        setChallenge(result.challenge);
      }
    } catch (failure) {
      setError(messageOf(failure));
    } finally {
      pending.current = false;
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit}>
      {challenge ? (
        <>
          <p role="status">
            If this address has workspace access, a code will arrive by email.
            It expires in 10 minutes.
          </p>
          <Field title="Sign-in code">
            <input
              value={code}
              onChange={(event) => setCode(event.target.value)}
              inputMode="numeric"
              autoComplete="one-time-code"
              pattern="[0-9]{6}"
              maxLength={6}
              autoFocus
              required
            />
          </Field>
        </>
      ) : (
        <Field title="Email address">
          <input
            type="email"
            autoComplete="email"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            maxLength={254}
            required
          />
        </Field>
      )}
      {error && <p role="alert">{error}</p>}
      <button type="submit" className="primary" disabled={busy || !online}>
        {challenge
          ? busy
            ? "Signing in…"
            : "Sign in"
          : busy
            ? "Requesting code…"
            : "Email a code"}
      </button>
      {challenge && (
        <button
          type="button"
          className="quiet"
          disabled={busy}
          onClick={() => {
            setChallenge("");
            setCode("");
            setError("");
          }}
        >
          Request another code
        </button>
      )}
    </form>
  );
}
