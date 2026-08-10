import { useCallback, useEffect, useRef, useState } from "react";

import type { LiveRouteApi } from "../api/client";
import type { GoogleNonceResponse, Session } from "../api/types";

const GOOGLE_IDENTITY_SCRIPT = "https://accounts.google.com/gsi/client";
const NONCE_REFRESH_MARGIN_MS = 30_000;

interface GoogleCredentialResponse {
  credential?: string;
}

interface GoogleIdentityAccounts {
  initialize(configuration: {
    client_id: string;
    nonce: string;
    callback: (response: GoogleCredentialResponse) => void;
  }): void;
  renderButton(
    parent: HTMLElement,
    options: {
      type: "standard";
      theme: "outline";
      size: "large";
      text: "signin_with";
      shape: "rectangular";
      width: number;
    },
  ): void;
}

interface GoogleIdentityGlobal {
  accounts: { id: GoogleIdentityAccounts };
}

declare global {
  interface Window {
    google?: GoogleIdentityGlobal;
  }
}

let scriptPromise: Promise<GoogleIdentityAccounts> | undefined;
let nonceCache:
  | {
      api: LiveRouteApi;
      promise: Promise<GoogleNonceResponse>;
      expiresAtUnixMS: number;
    }
  | undefined;

function loadGoogleIdentity(): Promise<GoogleIdentityAccounts> {
  if (window.google?.accounts.id) {
    return Promise.resolve(window.google.accounts.id);
  }
  if (scriptPromise) return scriptPromise;

  const pending = new Promise<GoogleIdentityAccounts>((resolve, reject) => {
    const existing = document.querySelector<HTMLScriptElement>(
      `script[src="${GOOGLE_IDENTITY_SCRIPT}"]`,
    );
    const script = existing ?? document.createElement("script");
    const loaded = (): void => {
      if (window.google?.accounts.id) {
        resolve(window.google.accounts.id);
      } else {
        reject(new Error("Google Identity Services did not initialize."));
      }
    };
    script.addEventListener("load", loaded, { once: true });
    script.addEventListener(
      "error",
      () => reject(new Error("Google Identity Services could not be loaded.")),
      { once: true },
    );
    if (!existing) {
      script.src = GOOGLE_IDENTITY_SCRIPT;
      script.async = true;
      script.defer = true;
      document.head.append(script);
    }
  }).catch((error: unknown) => {
    scriptPromise = undefined;
    throw error;
  });
  scriptPromise = pending;
  return pending;
}

function getLoginNonce(api: LiveRouteApi): Promise<GoogleNonceResponse> {
  const now = Date.now();
  if (
    nonceCache?.api === api &&
    nonceCache.expiresAtUnixMS > now + NONCE_REFRESH_MARGIN_MS
  ) {
    return nonceCache.promise;
  }

  const promise = api.createGoogleLoginNonce();
  nonceCache = {
    api,
    promise,
    expiresAtUnixMS: Number.POSITIVE_INFINITY,
  };
  void promise.then(
    (response) => {
      if (nonceCache?.promise === promise) {
        nonceCache.expiresAtUnixMS = response.expires_at_unix_ms;
      }
    },
    () => {
      if (nonceCache?.promise === promise) nonceCache = undefined;
    },
  );
  return promise;
}

function initialTimeZone(): string {
  const detected = Intl.DateTimeFormat().resolvedOptions().timeZone;
  return detected || "America/New_York";
}

interface GoogleSignInProps {
  api: LiveRouteApi;
  onAuthenticated(session: Session): void | Promise<void>;
  clientId?: string;
}

export function GoogleSignIn({
  api,
  onAuthenticated,
  clientId = import.meta.env.VITE_GOOGLE_WEB_CLIENT_ID ?? "",
}: GoogleSignInProps) {
  const button = useRef<HTMLDivElement>(null);
  const [timeZoneName, setTimeZoneName] = useState(initialTimeZone);
  const timeZoneNameRef = useRef(timeZoneName);
  timeZoneNameRef.current = timeZoneName;
  const [attempt, setAttempt] = useState(0);
  const [status, setStatus] = useState<
    "loading" | "ready" | "submitting" | "error"
  >(clientId ? "loading" : "error");
  const [error, setError] = useState(
    clientId ? "" : "Google sign-in is not configured for this deployment.",
  );

  const receiveCredential = useCallback(
    async (response: GoogleCredentialResponse): Promise<void> => {
      const credential = response.credential?.trim();
      if (!credential) {
        setError("Google did not return an identity credential. Please retry.");
        setStatus("error");
        return;
      }
      const selectedTimeZone = timeZoneNameRef.current.trim();
      if (!selectedTimeZone) {
        setError("Choose your home timezone before signing in.");
        setStatus("error");
        return;
      }

      nonceCache = undefined;
      setStatus("submitting");
      setError("");
      try {
        await onAuthenticated(
          await api.authenticateWithGoogle({
            credential,
            default_time_zone_name: selectedTimeZone,
          }),
        );
      } catch (authenticationError) {
        setError(
          authenticationError instanceof Error
            ? authenticationError.message
            : "Google sign-in failed.",
        );
        setStatus("error");
      }
    },
    [api, onAuthenticated],
  );

  useEffect(() => {
    if (!clientId) return;
    let active = true;
    let refreshTimer: number | undefined;

    const prepare = async (): Promise<void> => {
      setStatus("loading");
      setError("");
      try {
        const [identity, nonce] = await Promise.all([
          loadGoogleIdentity(),
          getLoginNonce(api),
        ]);
        if (!active || !button.current) return;
        identity.initialize({
          client_id: clientId,
          nonce: nonce.nonce,
          callback: (response) => void receiveCredential(response),
        });
        button.current.replaceChildren();
        identity.renderButton(button.current, {
          type: "standard",
          theme: "outline",
          size: "large",
          text: "signin_with",
          shape: "rectangular",
          width: 280,
        });
        setStatus("ready");

        const refreshAfter = Math.max(
          1_000,
          nonce.expires_at_unix_ms - Date.now() - NONCE_REFRESH_MARGIN_MS,
        );
        refreshTimer = window.setTimeout(() => {
          nonceCache = undefined;
          setAttempt((value) => value + 1);
        }, refreshAfter);
      } catch (preparationError) {
        if (!active) return;
        setError(
          preparationError instanceof Error
            ? preparationError.message
            : "Google sign-in could not be prepared.",
        );
        setStatus("error");
      }
    };

    void prepare();
    return () => {
      active = false;
      if (refreshTimer !== undefined) window.clearTimeout(refreshTimer);
    };
  }, [api, attempt, clientId, receiveCredential]);

  return (
    <div className="google-sign-in">
      <label htmlFor="login-time-zone">Home timezone</label>
      <input
        id="login-time-zone"
        value={timeZoneName}
        onChange={(event) => setTimeZoneName(event.target.value)}
        disabled={status === "submitting"}
        autoComplete="off"
      />
      {clientId ? (
        <div
          ref={button}
          aria-busy={status === "loading" || status === "submitting"}
          className="google-button-slot"
        />
      ) : (
        <button className="primary-button" type="button" disabled>
          Sign in with Google
        </button>
      )}
      {status === "loading" && <p role="status">Preparing Google sign-in…</p>}
      {status === "submitting" && <p role="status">Signing you in…</p>}
      {error && <p role="alert">{error}</p>}
      {status === "error" && clientId && (
        <button
          className="secondary-button"
          type="button"
          onClick={() => {
            nonceCache = undefined;
            setAttempt((value) => value + 1);
          }}
        >
          Retry Google sign-in
        </button>
      )}
    </div>
  );
}
