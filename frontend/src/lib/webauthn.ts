/**
 * The browser half of a security key: turning the server's options into what
 * `navigator.credentials` takes, and the credential it returns into JSON the
 * server checks.
 *
 * The server's options are WebAuthn's JSON form — every binary field base64url
 * — and the browser wants ArrayBuffers. `PublicKeyCredential.parse…FromJSON`
 * and `toJSON()` do this where they exist, but not in every browser an operator
 * signs in from, so it is done here, by hand, in a module with no React and no
 * DOM beyond the one call each ceremony makes.
 */

import type { WebAuthnProof } from '../api/types'

/** Whether this browser can ask a security key anything at all. */
export function webauthnSupported(): boolean {
  return (
    typeof window !== 'undefined' &&
    typeof window.PublicKeyCredential === 'function' &&
    typeof navigator !== 'undefined' &&
    typeof navigator.credentials?.create === 'function'
  )
}

export function fromBase64url(value: string): ArrayBuffer {
  const base64 = value.replace(/-/g, '+').replace(/_/g, '/')
  const padded = base64 + '='.repeat((4 - (base64.length % 4)) % 4)
  const binary = atob(padded)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
  return bytes.buffer
}

export function toBase64url(value: ArrayBuffer | ArrayBufferView): string {
  const bytes =
    value instanceof ArrayBuffer ? new Uint8Array(value) : new Uint8Array(value.buffer, value.byteOffset, value.byteLength)
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/** A credential descriptor as the server writes it. */
interface DescriptorJSON {
  type: PublicKeyCredentialType
  id: string
  transports?: AuthenticatorTransport[]
}

/** `PublicKeyCredentialCreationOptions` in WebAuthn's JSON form. */
export interface CreationOptionsJSON {
  rp: PublicKeyCredentialRpEntity
  user: { id: string; name: string; displayName: string }
  challenge: string
  pubKeyCredParams: PublicKeyCredentialParameters[]
  timeout?: number
  excludeCredentials?: DescriptorJSON[]
  authenticatorSelection?: AuthenticatorSelectionCriteria
  attestation?: AttestationConveyancePreference
}

/** `PublicKeyCredentialRequestOptions` in WebAuthn's JSON form. */
export interface RequestOptionsJSON {
  challenge: string
  timeout?: number
  rpId?: string
  allowCredentials?: DescriptorJSON[]
  userVerification?: UserVerificationRequirement
}

function descriptor(item: DescriptorJSON): PublicKeyCredentialDescriptor {
  return { type: item.type, id: fromBase64url(item.id), ...(item.transports ? { transports: item.transports } : {}) }
}

/**
 * The options `navigator.credentials.create` takes. Extensions are never
 * passed on: the server asks for none, and refuses an answer carrying any.
 */
export function creationOptions(json: CreationOptionsJSON): PublicKeyCredentialCreationOptions {
  return {
    rp: json.rp,
    user: { ...json.user, id: fromBase64url(json.user.id) },
    challenge: fromBase64url(json.challenge),
    pubKeyCredParams: json.pubKeyCredParams,
    ...(json.timeout ? { timeout: json.timeout } : {}),
    excludeCredentials: (json.excludeCredentials ?? []).map(descriptor),
    ...(json.authenticatorSelection ? { authenticatorSelection: json.authenticatorSelection } : {}),
    ...(json.attestation ? { attestation: json.attestation } : {}),
  }
}

export function requestOptions(json: RequestOptionsJSON): PublicKeyCredentialRequestOptions {
  return {
    challenge: fromBase64url(json.challenge),
    ...(json.timeout ? { timeout: json.timeout } : {}),
    ...(json.rpId ? { rpId: json.rpId } : {}),
    allowCredentials: (json.allowCredentials ?? []).map(descriptor),
    ...(json.userVerification ? { userVerification: json.userVerification } : {}),
  }
}

/** The part of a `PublicKeyCredential` the two serialisers read. */
export interface CredentialLike {
  id: string
  rawId: ArrayBuffer
  type: string
  authenticatorAttachment?: string | null
  response: AuthenticatorResponse
}

/**
 * What a key made, as the JSON the server parses. The client extension results
 * are sent empty: none were asked for, and a browser or password manager that
 * volunteers some would have the registration refused for it.
 */
export function registrationJSON(credential: CredentialLike): Record<string, unknown> {
  const response = credential.response as AuthenticatorAttestationResponse
  const transports = typeof response.getTransports === 'function' ? response.getTransports() : []
  return {
    id: credential.id,
    rawId: toBase64url(credential.rawId),
    type: credential.type,
    ...(credential.authenticatorAttachment ? { authenticatorAttachment: credential.authenticatorAttachment } : {}),
    response: {
      clientDataJSON: toBase64url(response.clientDataJSON),
      attestationObject: toBase64url(response.attestationObject),
      transports,
    },
    clientExtensionResults: {},
  }
}

/** A key's signature, as the JSON the server checks. */
export function assertionJSON(credential: CredentialLike): Record<string, unknown> {
  const response = credential.response as AuthenticatorAssertionResponse
  return {
    id: credential.id,
    rawId: toBase64url(credential.rawId),
    type: credential.type,
    ...(credential.authenticatorAttachment ? { authenticatorAttachment: credential.authenticatorAttachment } : {}),
    response: {
      clientDataJSON: toBase64url(response.clientDataJSON),
      authenticatorData: toBase64url(response.authenticatorData),
      signature: toBase64url(response.signature),
      ...(response.userHandle ? { userHandle: toBase64url(response.userHandle) } : {}),
    },
    clientExtensionResults: {},
  }
}

/** Asks the key to make a credential — the browser's own prompt. */
export async function createKeyCredential(options: { publicKey: CreationOptionsJSON }): Promise<Record<string, unknown>> {
  const credential = (await navigator.credentials.create({
    publicKey: creationOptions(options.publicKey),
  })) as CredentialLike | null
  if (!credential) throw new Error('The browser returned no key.')
  return registrationJSON(credential)
}

/** Asks one of the account's keys to sign a ceremony, and returns the proof. */
export async function signWithKey(ceremony: {
  ceremony: string
  options: { publicKey: RequestOptionsJSON }
}): Promise<WebAuthnProof> {
  const credential = (await navigator.credentials.get({
    publicKey: requestOptions(ceremony.options.publicKey),
  })) as CredentialLike | null
  if (!credential) throw new Error('The browser returned no key.')
  return { ceremony: ceremony.ceremony, credential: assertionJSON(credential) }
}

/**
 * The browser's refusals in words somebody can act on. They arrive as
 * DOMExceptions whose own messages differ by browser and say little.
 */
export function keyErrorText(err: unknown): string | null {
  const name = err instanceof DOMException || err instanceof Error ? err.name : ''
  switch (name) {
    case 'NotAllowedError':
      return 'The key did not answer — the prompt was closed or timed out. Try again.'
    case 'InvalidStateError':
      return 'That key is already registered to your account.'
    case 'SecurityError':
      return 'This browser will not use a key on this address. Open kubemg at its public URL.'
    case 'NotSupportedError':
      return 'This key cannot make the kind of credential kubemg asks for.'
    case 'AbortError':
      return 'The key check was cancelled.'
  }
  return null
}
