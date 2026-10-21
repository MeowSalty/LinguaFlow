import { describe, expect, it, vi } from 'vitest'
import {
  actionAllowed,
  bindingIdOf,
  isProofRejection,
  proofIsFresh,
  proofMatches,
  reauthCandidates,
  type LoginMethods,
} from '../../src/components/auth/security/proof'
import { ApiError } from '../../src/api/utils'
vi.mock('@/i18n', () => ({ t: (key: string) => key }))

const methods = (): LoginMethods => ({
  password: { is_set: true, verified: true, can_login: true, can_set: false, can_delete: true },
  email: { address: null, verified: false, can_recover: false },
  bindings: [
    {
      binding_id: 7,
      connection_id: 'oidc',
      display_name: 'OIDC',
      can_login: false,
      can_reauthenticate: true,
      can_unlink: true,
      verified: true,
      archived: false,
    },
    {
      binding_id: 8,
      connection_id: 'oauth',
      display_name: 'OAuth',
      can_login: true,
      can_reauthenticate: false,
      can_unlink: true,
      verified: true,
      archived: false,
    },
  ],
  available_connections: [{ connection_id: 'new', display_name: 'New', can_link: true }],
})
describe('security action proofs', () => {
  it.each([
    '',
    '0',
    '-1',
    '1.0',
    '1e2',
    '01',
    ' 1',
    '+1',
    'Infinity',
    '9007199254740992',
    '9007199254740993',
  ])('rejects noncanonical or unsafe binding ID %s', (value) => {
    expect(bindingIdOf(value)).toBeNull()
  })
  it('accepts safe positive decimal binding IDs', () => {
    expect(bindingIdOf('7')).toBe(7)
    expect(bindingIdOf(String(Number.MAX_SAFE_INTEGER))).toBe(Number.MAX_SAFE_INTEGER)
  })
  it('never authorizes removing a credential with that same credential', () => {
    expect(reauthCandidates(methods(), 'password_delete', 'password')).toEqual([
      { kind: 'binding', bindingId: 7, label: 'OIDC' },
    ])
    expect(reauthCandidates(methods(), 'unlink', '7')).toEqual([
      { kind: 'password', bindingId: null, label: '' },
    ])
  })
  it('uses reauthentication capability independently of external login capability', () => {
    expect(reauthCandidates(methods(), 'link', 'new').map((item) => item.bindingId)).toEqual([
      null,
      7,
    ])
    const noPassword = methods()
    noPassword.password.can_login = false
    expect(reauthCandidates(noPassword, 'unlink', '7')).toEqual([])
  })
  it('never sends a rounded unsafe binding ID for external verification', () => {
    const state = methods()
    state.bindings[0]!.binding_id = Number.MAX_SAFE_INTEGER + 1
    expect(reauthCandidates(state, 'password_delete', 'password')).toEqual([])
  })
  it('checks action, exact target and expiry separately', () => {
    const proof = { action: 'unlink' as const, targetId: '7', expiresAt: '2026-10-13T12:00:00Z' }
    expect(proofMatches(proof, 'unlink', '7')).toBe(true)
    expect(proofMatches(proof, 'link', '7')).toBe(false)
    expect(proofMatches(proof, 'unlink', '8')).toBe(false)
    expect(proofIsFresh(proof, Date.parse(proof.expiresAt))).toBe(false)
    expect(proofIsFresh(proof, Date.parse(proof.expiresAt) - 1)).toBe(true)
    expect(proofIsFresh({ expiresAt: 'invalid' })).toBe(false)
  })
  it('rejects resumed operations when the capability or target changed', () => {
    const state = methods()
    expect(actionAllowed(state, 'password_set', 'password')).toBe(false)
    expect(actionAllowed(state, 'password_delete', 'password')).toBe(true)
    expect(actionAllowed(state, 'password_delete', 'other')).toBe(false)
    expect(actionAllowed(state, 'unlink', '7')).toBe(true)
    expect(actionAllowed(state, 'unlink', '07')).toBe(false)
    state.bindings[0]!.can_unlink = false
    expect(actionAllowed(state, 'unlink', '7')).toBe(false)
    expect(actionAllowed(state, 'link', 'new')).toBe(true)
    state.available_connections[0]!.can_link = false
    expect(actionAllowed(state, 'link', 'new')).toBe(false)
  })
  it('treats only stable reauthentication-required as rejected proof', () => {
    expect(
      isProofRejection(
        new ApiError('proof', 403, {
          type: 'urn:linguaflow:reauthentication-required',
          title: 'reauthentication_required',
        }),
      ),
    ).toBe(true)
    expect(
      isProofRejection(
        new ApiError('forbidden', 403, {
          type: 'urn:linguaflow:forbidden',
          title: 'forbidden',
        }),
      ),
    ).toBe(false)
    expect(isProofRejection(new ApiError('plain forbidden', 403))).toBe(false)
    expect(isProofRejection(new ApiError('unauthorized', 401))).toBe(false)
    expect(isProofRejection(new Error('network'))).toBe(false)
  })
})
