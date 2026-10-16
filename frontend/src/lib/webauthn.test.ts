import { describe, expect, it } from 'vitest'

import {
  assertionJSON,
  creationOptions,
  fromBase64url,
  keyErrorText,
  registrationJSON,
  requestOptions,
  toBase64url,
} from './webauthn'
import type { CredentialLike } from './webauthn'

const bytes = (...values: number[]) => new Uint8Array(values).buffer

describe('base64url', () => {
  it('round-trips bytes that need the url alphabet and no padding', () => {
    // 0xfb 0xff encodes to "+/8=" in plain base64.
    const raw = bytes(0xfb, 0xff)
    expect(toBase64url(raw)).toBe('-_8')
    expect(new Uint8Array(fromBase64url('-_8'))).toEqual(new Uint8Array(raw))
  })

  it('reads every length the server writes', () => {
    for (let n = 0; n < 8; n++) {
      const raw = new Uint8Array(Array.from({ length: n }, (_, i) => i * 37))
      expect(new Uint8Array(fromBase64url(toBase64url(raw)))).toEqual(raw)
    }
  })
})

describe('the server’s options, as the browser takes them', () => {
  it('turns every binary field of a registration into bytes and passes no extensions', () => {
    const options = creationOptions({
      rp: { id: 'kubemg.example.com', name: 'KubeMG' },
      user: { id: 'AAAAAAAAAAc', name: 'dev', displayName: 'dev' },
      challenge: 'AQID',
      pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
      timeout: 120000,
      excludeCredentials: [{ type: 'public-key', id: 'BAUG', transports: ['usb'] }],
      attestation: 'none',
      // A field the browser must never be handed back, as if the server sent one.
      ...({ extensions: { credProps: true } } as object),
    })
    expect(new Uint8Array(options.challenge as ArrayBuffer)).toEqual(new Uint8Array([1, 2, 3]))
    expect(new Uint8Array(options.user.id as ArrayBuffer)).toEqual(new Uint8Array([0, 0, 0, 0, 0, 0, 0, 7]))
    expect(new Uint8Array(options.excludeCredentials?.[0].id as ArrayBuffer)).toEqual(new Uint8Array([4, 5, 6]))
    expect(options.excludeCredentials?.[0].transports).toEqual(['usb'])
    expect(options.rp.id).toBe('kubemg.example.com')
    expect('extensions' in options).toBe(false)
  })

  it('names the keys an assertion may be answered with', () => {
    const options = requestOptions({
      challenge: 'AQID',
      rpId: 'kubemg.example.com',
      allowCredentials: [{ type: 'public-key', id: 'BAUG' }],
      userVerification: 'discouraged',
    })
    expect(new Uint8Array(options.allowCredentials?.[0].id as ArrayBuffer)).toEqual(new Uint8Array([4, 5, 6]))
    expect(options.rpId).toBe('kubemg.example.com')
    expect(options.userVerification).toBe('discouraged')
  })
})

describe('a key’s answer, as the server parses it', () => {
  it('serialises a registration with its transports and no extension results', () => {
    const credential = {
      id: 'BAUG',
      rawId: bytes(4, 5, 6),
      type: 'public-key',
      authenticatorAttachment: 'cross-platform',
      response: {
        clientDataJSON: bytes(1),
        attestationObject: bytes(2),
        getTransports: () => ['usb', 'nfc'],
      },
    } as unknown as CredentialLike
    expect(registrationJSON(credential)).toEqual({
      id: 'BAUG',
      rawId: 'BAUG',
      type: 'public-key',
      authenticatorAttachment: 'cross-platform',
      response: { clientDataJSON: 'AQ', attestationObject: 'Ag', transports: ['usb', 'nfc'] },
      clientExtensionResults: {},
    })
  })

  it('serialises a signature, leaving out a user handle the key did not return', () => {
    const credential = {
      id: 'BAUG',
      rawId: bytes(4, 5, 6),
      type: 'public-key',
      response: { clientDataJSON: bytes(1), authenticatorData: bytes(2), signature: bytes(3), userHandle: null },
    } as unknown as CredentialLike
    expect(assertionJSON(credential)).toEqual({
      id: 'BAUG',
      rawId: 'BAUG',
      type: 'public-key',
      response: { clientDataJSON: 'AQ', authenticatorData: 'Ag', signature: 'Aw' },
      clientExtensionResults: {},
    })
  })
})

describe('the browser’s refusals', () => {
  it('reads a dismissed prompt and a key already registered in words', () => {
    expect(keyErrorText(new DOMException('x', 'NotAllowedError'))).toMatch(/closed or timed out/)
    expect(keyErrorText(new DOMException('x', 'InvalidStateError'))).toMatch(/already registered/)
    expect(keyErrorText(new Error('network'))).toBeNull()
  })
})
