/**
 * Passkeys in the browser.
 *
 * WebAuthn speaks ArrayBuffers and the API speaks JSON, so something has to
 * translate between them. That happens here, once, rather than in each screen
 * that touches a credential: the encoding is base64url in both directions and
 * getting it wrong produces an authenticator error with no useful message.
 *
 * The conversion is written out rather than delegated to
 * `PublicKeyCredential.parseCreationOptionsFromJSON`, which browsers gained
 * between Chrome 119 and Firefox 135. A feature-detected fast path would mean
 * two encoders where only the one the developer's browser takes is ever
 * exercised, and the other is discovered by an operator who cannot sign in.
 */

import { apiFetch } from "@/lib/api";

/**
 * Whether this browser can do WebAuthn at all.
 *
 * Called before offering anything: a button that opens a dialog the browser
 * cannot show is worse than an absent button, because the operator has no way
 * to tell a missing feature from a broken one.
 */
export function passkeysSupported(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.PublicKeyCredential === "function" &&
    typeof navigator.credentials?.create === "function"
  );
}

/** Decodes base64url, which is what the API sends and `atob` does not accept. */
function fromBase64Url(value: string): ArrayBuffer {
  const padded = value.replace(/-/g, "+").replace(/_/g, "/");
  const binary = atob(padded.padEnd(padded.length + ((4 - (padded.length % 4)) % 4), "="));
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index);
  }
  return bytes.buffer;
}

/** Encodes base64url without padding, which is what the API parses. */
function toBase64Url(value: ArrayBuffer): string {
  const bytes = new Uint8Array(value);
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

type CredentialDescriptorJson = { id: string; type: string; transports?: string[] };

type CreationOptionsJson = {
  publicKey: {
    challenge: string;
    user: { id: string; name: string; displayName: string };
    excludeCredentials?: CredentialDescriptorJson[];
    [key: string]: unknown;
  };
};

type RequestOptionsJson = {
  publicKey: {
    challenge: string;
    allowCredentials?: CredentialDescriptorJson[];
    [key: string]: unknown;
  };
};

function decodeDescriptors(items?: CredentialDescriptorJson[]): PublicKeyCredentialDescriptor[] {
  return (items ?? []).map((item) => ({
    id: fromBase64Url(item.id),
    transports: item.transports as AuthenticatorTransport[] | undefined,
    type: "public-key",
  }));
}

/**
 * Registers a new passkey for the signed-in operator.
 *
 * The label travels as a query parameter because the body is the credential
 * itself and has a shape the server parses as a whole.
 */
export async function registerPasskey(label: string): Promise<void> {
  const options = await apiFetch<CreationOptionsJson>("/v1/panel/auth/passkeys/register/begin", {
    method: "POST",
  });

  const created = (await navigator.credentials.create({
    publicKey: {
      ...options.publicKey,
      challenge: fromBase64Url(options.publicKey.challenge),
      excludeCredentials: decodeDescriptors(options.publicKey.excludeCredentials),
      user: {
        ...options.publicKey.user,
        id: fromBase64Url(options.publicKey.user.id),
      },
    } as PublicKeyCredentialCreationOptions,
  })) as PublicKeyCredential | null;

  if (!created) {
    throw new Error("passkey creation returned nothing");
  }
  const response = created.response as AuthenticatorAttestationResponse;

  await apiFetch(`/v1/panel/auth/passkeys/register/finish?label=${encodeURIComponent(label)}`, {
    body: JSON.stringify({
      authenticatorAttachment: created.authenticatorAttachment ?? undefined,
      clientExtensionResults: created.getClientExtensionResults(),
      id: created.id,
      rawId: toBase64Url(created.rawId),
      response: {
        attestationObject: toBase64Url(response.attestationObject),
        clientDataJSON: toBase64Url(response.clientDataJSON),
        // Recorded so a later sign-in can tell the browser where to look. An
        // authenticator that predates the call simply reports nothing.
        transports: response.getTransports?.() ?? [],
      },
      type: created.type,
    }),
    method: "POST",
  });
}

/** What a completed passkey sign-in returns, matching the password path. */
export type PasskeyLoginResult = { csrfToken: string };

/**
 * Signs in with a passkey, with no password and no second factor.
 *
 * The credential is discoverable, so the authenticator knows which account it
 * belongs to and the operator never types an email address.
 */
export async function signInWithPasskey(): Promise<PasskeyLoginResult> {
  const options = await apiFetch<RequestOptionsJson>("/v1/panel/auth/passkey/login/begin", {
    method: "POST",
  });

  const assertion = (await navigator.credentials.get({
    publicKey: {
      ...options.publicKey,
      allowCredentials: decodeDescriptors(options.publicKey.allowCredentials),
      challenge: fromBase64Url(options.publicKey.challenge),
    } as PublicKeyCredentialRequestOptions,
  })) as PublicKeyCredential | null;

  if (!assertion) {
    throw new Error("passkey assertion returned nothing");
  }
  const response = assertion.response as AuthenticatorAssertionResponse;

  return apiFetch<PasskeyLoginResult>("/v1/panel/auth/passkey/login/finish", {
    body: JSON.stringify({
      clientExtensionResults: assertion.getClientExtensionResults(),
      id: assertion.id,
      rawId: toBase64Url(assertion.rawId),
      response: {
        authenticatorData: toBase64Url(response.authenticatorData),
        clientDataJSON: toBase64Url(response.clientDataJSON),
        signature: toBase64Url(response.signature),
        userHandle: response.userHandle ? toBase64Url(response.userHandle) : undefined,
      },
      type: assertion.type,
    }),
    method: "POST",
  });
}

/**
 * Whether an aborted ceremony is the operator declining rather than a fault.
 *
 * Closing the browser's dialog raises `NotAllowedError`, which is the normal
 * way to say "not now". Reporting that as an error would put a red banner on
 * screen every time somebody changed their mind.
 */
export function passkeyDismissed(error: unknown): boolean {
  return (
    error instanceof DOMException &&
    (error.name === "NotAllowedError" || error.name === "AbortError")
  );
}
