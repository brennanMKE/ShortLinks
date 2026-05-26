// Unit tests for the WebAuthn browser glue. These cover the encoding contract
// with the Go server (go-webauthn's URLEncodedBase64 == base64.RawURLEncoding):
// the base64url round-trip, decoding the server's request options, and
// serializing an assertion back into the exact JSON keys /auth/login/finish
// parses. There is no browser/authenticator here, so the actual ceremony
// (navigator.credentials.get) is not exercised — only the pure data shaping.

import { describe, it, expect } from 'vitest';
import {
  bufferToBase64url,
  base64urlToBytes,
  toPublicKeyRequestOptions,
  serializeAssertion,
  type ServerRequestOptions,
} from './webauthn';

function bytes(...vals: number[]): Uint8Array {
  return new Uint8Array(vals);
}

describe('base64url round-trip', () => {
  it('encode∘decode is the identity for arbitrary bytes', () => {
    for (let len = 0; len < 40; len++) {
      const input = new Uint8Array(len);
      for (let i = 0; i < len; i++) input[i] = (i * 37 + 11) & 0xff;
      const round = base64urlToBytes(bufferToBase64url(input));
      expect(Array.from(round)).toEqual(Array.from(input));
    }
  });

  it('produces unpadded, url-safe output (matches Go RawURLEncoding)', () => {
    // 0xFB 0xFF 0xBF -> std base64 "+/+/", url-safe "-_-_", no padding.
    const encoded = bufferToBase64url(bytes(0xfb, 0xff, 0xbf));
    expect(encoded).toBe('-_-_');
    expect(encoded).not.toContain('=');
    expect(encoded).not.toContain('+');
    expect(encoded).not.toContain('/');
  });

  it('matches a known vector ("hello" / no padding)', () => {
    // "hello" -> "aGVsbG8" in unpadded base64url (std base64 is "aGVsbG8=").
    const hello = new TextEncoder().encode('hello');
    expect(bufferToBase64url(hello)).toBe('aGVsbG8');
    expect(new TextDecoder().decode(base64urlToBytes('aGVsbG8'))).toBe('hello');
  });

  it('decodes one- and two-byte tail lengths correctly', () => {
    // 1 byte -> 2 chars, 2 bytes -> 3 chars (RawURLEncoding tail handling).
    expect(bufferToBase64url(bytes(0x66))).toBe('Zg'); // "f"
    expect(bufferToBase64url(bytes(0x66, 0x6f))).toBe('Zm8'); // "fo"
    expect(Array.from(base64urlToBytes('Zg'))).toEqual([0x66]);
    expect(Array.from(base64urlToBytes('Zm8'))).toEqual([0x66, 0x6f]);
  });

  it('tolerates input that still carries std-base64 padding', () => {
    expect(new TextDecoder().decode(base64urlToBytes('aGVsbG8='))).toBe('hello');
  });
});

describe('toPublicKeyRequestOptions', () => {
  it('decodes challenge and each allowCredentials id into bytes', () => {
    const server: ServerRequestOptions = {
      // base64url of [1,2,3,4] and [0xaa,0xbb] respectively.
      challenge: bufferToBase64url(bytes(1, 2, 3, 4)),
      timeout: 60000,
      rpId: 'go.sstools.co',
      userVerification: 'required',
      allowCredentials: [
        {
          type: 'public-key',
          id: bufferToBase64url(bytes(0xaa, 0xbb)),
          transports: ['internal', 'hybrid'],
        },
      ],
    };

    const opts = toPublicKeyRequestOptions(server);

    expect(Array.from(new Uint8Array(opts.challenge as ArrayBuffer))).toEqual([1, 2, 3, 4]);
    expect(opts.timeout).toBe(60000);
    expect(opts.rpId).toBe('go.sstools.co');
    expect(opts.userVerification).toBe('required');
    expect(opts.allowCredentials).toHaveLength(1);
    const cred = opts.allowCredentials![0];
    expect(cred.type).toBe('public-key');
    expect(Array.from(new Uint8Array(cred.id as ArrayBuffer))).toEqual([0xaa, 0xbb]);
    expect(cred.transports).toEqual(['internal', 'hybrid']);
  });

  it('omits allowCredentials for a discoverable (conditional-UI) challenge', () => {
    const opts = toPublicKeyRequestOptions({
      challenge: bufferToBase64url(bytes(9, 9, 9)),
    });
    expect(opts.allowCredentials).toBeUndefined();
    expect(Array.from(new Uint8Array(opts.challenge as ArrayBuffer))).toEqual([9, 9, 9]);
  });
});

describe('serializeAssertion', () => {
  // A minimal fake of the PublicKeyCredential returned by navigator.credentials.get().
  function fakeCredential(opts: {
    rawId: Uint8Array;
    clientDataJSON: Uint8Array;
    authenticatorData: Uint8Array;
    signature: Uint8Array;
    userHandle?: Uint8Array | null;
    attachment?: string;
  }): PublicKeyCredential {
    const response = {
      clientDataJSON: opts.clientDataJSON.buffer,
      authenticatorData: opts.authenticatorData.buffer,
      signature: opts.signature.buffer,
      userHandle: opts.userHandle === undefined ? null : opts.userHandle,
    };
    return {
      id: bufferToBase64url(opts.rawId),
      type: 'public-key',
      rawId: opts.rawId.buffer,
      authenticatorAttachment: opts.attachment ?? null,
      response,
      getClientExtensionResults: () => ({}),
    } as unknown as PublicKeyCredential;
  }

  it('produces the exact base64url JSON keys /auth/login/finish expects', () => {
    const rawId = bytes(0xde, 0xad, 0xbe, 0xef);
    const clientData = new TextEncoder().encode('{"type":"webauthn.get"}');
    const authData = bytes(1, 2, 3, 4, 5);
    const sig = bytes(0x30, 0x44, 0x02);
    const userHandle = bytes(0x10, 0x20, 0x30);

    const payload = serializeAssertion(
      fakeCredential({
        rawId,
        clientDataJSON: clientData,
        authenticatorData: authData,
        signature: sig,
        userHandle,
        attachment: 'platform',
      }),
    );

    expect(payload).toEqual({
      id: bufferToBase64url(rawId),
      type: 'public-key',
      rawId: bufferToBase64url(rawId),
      authenticatorAttachment: 'platform',
      clientExtensionResults: {},
      response: {
        clientDataJSON: bufferToBase64url(clientData),
        authenticatorData: bufferToBase64url(authData),
        signature: bufferToBase64url(sig),
        userHandle: bufferToBase64url(userHandle),
      },
    });

    // Every binary field must be decodable back to the original bytes by the
    // server's base64url decoder.
    expect(Array.from(base64urlToBytes(payload.rawId))).toEqual(Array.from(rawId));
    expect(Array.from(base64urlToBytes(payload.response.signature))).toEqual(Array.from(sig));
  });

  it('omits userHandle when the authenticator returns none', () => {
    const payload = serializeAssertion(
      fakeCredential({
        rawId: bytes(1),
        clientDataJSON: bytes(2),
        authenticatorData: bytes(3),
        signature: bytes(4),
        userHandle: null,
      }),
    );
    expect('userHandle' in payload.response).toBe(false);
  });

  it('omits userHandle when it is an empty buffer', () => {
    const payload = serializeAssertion(
      fakeCredential({
        rawId: bytes(1),
        clientDataJSON: bytes(2),
        authenticatorData: bytes(3),
        signature: bytes(4),
        userHandle: new Uint8Array(0),
      }),
    );
    expect('userHandle' in payload.response).toBe(false);
  });
});
