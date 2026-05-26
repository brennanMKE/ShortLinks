// Browser glue for the WebAuthn authentication ceremony. The server speaks the
// go-webauthn protocol JSON: every binary field (challenge, credential ids,
// authenticatorData, clientDataJSON, signature, userHandle) is encoded as
// *unpadded base64url* (Go's `base64.RawURLEncoding`, the `URLEncodedBase64`
// type in github.com/go-webauthn/webauthn/protocol). The browser, however,
// hands us / wants `ArrayBuffer`s. These helpers translate between the two and
// MUST match the server's encoding exactly — a mismatch (std base64 vs.
// base64url, or padded vs. unpadded) silently breaks the ceremony.
//
// See internal/auth/login.go (StartLogin / FinishLogin) and
// internal/handlers/auth.go (LoginStart / LoginFinish) for the server side.

// ── base64url ↔ ArrayBuffer ─────────────────────────────────────────────────

/**
 * Encode bytes as unpadded base64url (RFC 4648 §5, no `=` padding). Mirrors Go's
 * `base64.RawURLEncoding.EncodeToString`, which is what the server uses for
 * every binary field it parses back from the assertion.
 */
export function bufferToBase64url(buffer: ArrayBuffer | Uint8Array): string {
  const bytes = buffer instanceof Uint8Array ? buffer : new Uint8Array(buffer);
  let binary = '';
  for (let i = 0; i < bytes.length; i++) {
    binary += String.fromCharCode(bytes[i]);
  }
  // btoa yields standard base64 with `+`/`/` and `=` padding; convert to the
  // url-safe alphabet and strip padding to match RawURLEncoding.
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/**
 * Decode an unpadded (or padded) base64url string into a `Uint8Array`. Accepts
 * the standard alphabet too so it is tolerant of either form, but the server
 * only ever emits unpadded base64url.
 */
export function base64urlToBytes(value: string): Uint8Array<ArrayBuffer> {
  // Normalize url-safe alphabet back to standard and re-pad for atob.
  const normalized = value.replace(/-/g, '+').replace(/_/g, '/');
  const padded = normalized.padEnd(
    normalized.length + ((4 - (normalized.length % 4)) % 4),
    '=',
  );
  const binary = atob(padded);
  // Back the view with a concrete ArrayBuffer (not ArrayBufferLike) so the
  // result satisfies the DOM `BufferSource` type used by WebAuthn options.
  const bytes = new Uint8Array(new ArrayBuffer(binary.length));
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

// ── Server request-options JSON → PublicKeyCredentialRequestOptions ──────────

/**
 * The shape of the assertion options the server returns from
 * `GET /auth/login/start`. Matches go-webauthn's `protocol.CredentialAssertion`:
 * a `publicKey` object (the `PublicKeyCredentialRequestOptions`) plus an optional
 * top-level `mediation`. `challenge` and each `allowCredentials[].id` are
 * unpadded base64url strings.
 */
export interface ServerCredentialAssertion {
  publicKey: ServerRequestOptions;
  mediation?: string;
}

/** The `publicKey` member of {@link ServerCredentialAssertion}. */
export interface ServerRequestOptions {
  challenge: string;
  timeout?: number;
  rpId?: string;
  allowCredentials?: ServerCredentialDescriptor[];
  userVerification?: UserVerificationRequirement;
  extensions?: Record<string, unknown>;
}

/** One entry of the server's `allowCredentials` list. */
export interface ServerCredentialDescriptor {
  type: PublicKeyCredentialType;
  id: string;
  transports?: AuthenticatorTransport[];
}

/**
 * Convert the server's JSON request options into the
 * `PublicKeyCredentialRequestOptions` the browser's `navigator.credentials.get`
 * expects, decoding `challenge` and every `allowCredentials[].id` from base64url
 * into `ArrayBuffer`s (`BufferSource`).
 */
export function toPublicKeyRequestOptions(
  options: ServerRequestOptions,
): PublicKeyCredentialRequestOptions {
  const publicKey: PublicKeyCredentialRequestOptions = {
    challenge: base64urlToBytes(options.challenge),
  };

  if (options.timeout !== undefined) publicKey.timeout = options.timeout;
  if (options.rpId !== undefined) publicKey.rpId = options.rpId;
  if (options.userVerification !== undefined) {
    publicKey.userVerification = options.userVerification;
  }
  if (options.extensions !== undefined) {
    publicKey.extensions = options.extensions as AuthenticationExtensionsClientInputs;
  }
  if (options.allowCredentials && options.allowCredentials.length > 0) {
    publicKey.allowCredentials = options.allowCredentials.map((cred) => {
      const descriptor: PublicKeyCredentialDescriptor = {
        type: cred.type,
        id: base64urlToBytes(cred.id),
      };
      if (cred.transports) descriptor.transports = cred.transports;
      return descriptor;
    });
  }

  return publicKey;
}

// ── PublicKeyCredential assertion → server finish JSON ───────────────────────

/**
 * The exact JSON body `POST /auth/login/finish` expects. Matches go-webauthn's
 * `protocol.CredentialAssertionResponse`: `id`/`rawId` and every
 * `response.*` binary field are unpadded base64url. `userHandle` is omitted when
 * the authenticator returns none (`omitempty` on the server).
 */
export interface AssertionFinishPayload {
  id: string;
  type: string;
  rawId: string;
  authenticatorAttachment?: string;
  clientExtensionResults: AuthenticationExtensionsClientOutputs;
  response: {
    clientDataJSON: string;
    authenticatorData: string;
    signature: string;
    userHandle?: string;
  };
}

/**
 * Serialize the `PublicKeyCredential` returned by `navigator.credentials.get()`
 * into the base64url JSON shape the server parses with
 * `protocol.ParseCredentialRequestResponse`. Encodes `id`/`rawId` and each
 * `response.*` field to match the server's `URLEncodedBase64` decoding exactly.
 */
export function serializeAssertion(
  credential: PublicKeyCredential,
): AssertionFinishPayload {
  const response = credential.response as AuthenticatorAssertionResponse;

  const payload: AssertionFinishPayload = {
    id: credential.id,
    type: credential.type,
    rawId: bufferToBase64url(credential.rawId),
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      clientDataJSON: bufferToBase64url(response.clientDataJSON),
      authenticatorData: bufferToBase64url(response.authenticatorData),
      signature: bufferToBase64url(response.signature),
    },
  };

  if (credential.authenticatorAttachment) {
    payload.authenticatorAttachment = credential.authenticatorAttachment;
  }
  // userHandle is optional; only include it when the authenticator returned one.
  if (response.userHandle && response.userHandle.byteLength > 0) {
    payload.response.userHandle = bufferToBase64url(response.userHandle);
  }

  return payload;
}

/**
 * Feature-detect conditional-mediation (passkey autofill) support. Browsers that
 * lack it should skip the background `mediation: 'conditional'` get() call and
 * rely on the explicit "Sign in" button instead.
 */
export async function conditionalMediationAvailable(): Promise<boolean> {
  const pk = (
    window as unknown as {
      PublicKeyCredential?: {
        isConditionalMediationAvailable?: () => Promise<boolean>;
      };
    }
  ).PublicKeyCredential;
  if (!pk || typeof pk.isConditionalMediationAvailable !== 'function') {
    return false;
  }
  try {
    return await pk.isConditionalMediationAvailable();
  } catch {
    return false;
  }
}
