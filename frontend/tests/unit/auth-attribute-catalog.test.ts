import { describe, expect, it } from 'vitest'
import {
  AUTH_ATTRIBUTE_MAX_MAPPINGS,
  AUTH_ATTRIBUTE_TYPES,
  AuthAttributeCatalogCache,
  attributeSourcesForKind,
  authAttributeCatalogCacheKey,
  authCatalogCandidateFingerprint,
  authSignupRequirementsFingerprint,
  cloneAuthAttributeMappings,
  createAuthAttributeMappingDraft,
  externalCatalogSourceKey,
  localCatalogSourceKey,
  validateAuthAttributeMappings,
  type AuthAttributeMapping,
} from '@/components/admin/authentication/auth-attribute-catalog'

const mapping = (field_id = 'custom.department'): AuthAttributeMapping => ({
  ...createAuthAttributeMappingDraft('oidc'),
  field_id,
  label: 'Department',
  pointer: '/department',
})

describe('attribute mappings', () => {
  it('offers six types and protocol-specific sources without derived creation', () => {
    expect(AUTH_ATTRIBUTE_TYPES).toEqual([
      'string',
      'boolean',
      'integer',
      'number',
      'datetime',
      'string_list',
    ])
    expect(attributeSourcesForKind('oidc')).toEqual(['oidc_id_token', 'oidc_userinfo'])
    expect(attributeSourcesForKind('generic_oauth2')).toEqual(['oauth2_profile'])
    expect(createAuthAttributeMappingDraft('generic_oauth2').source).toBe('oauth2_profile')
    expect(createAuthAttributeMappingDraft('oidc').source).toBe('oidc_id_token')
  })

  it('rejects empty required fields, duplicate ids and incompatible sources', () => {
    expect(
      validateAuthAttributeMappings([createAuthAttributeMappingDraft('oidc')], 'oidc').valid,
    ).toBe(false)
    expect(validateAuthAttributeMappings([mapping(), mapping()], 'oidc').rows).toEqual([
      { index: 1, code: 'fieldIdDuplicate' },
    ])
    expect(validateAuthAttributeMappings([mapping()], 'generic_oauth2').rows).toEqual([
      { index: 0, code: 'sourceNotAllowed' },
    ])
    expect(
      validateAuthAttributeMappings([{ ...mapping(), pointer: '' }], 'oidc').rows[0]?.code,
    ).toBe('pointerRequired')
    expect(validateAuthAttributeMappings([{ ...mapping(), label: '' }], 'oidc').rows[0]?.code).toBe(
      'labelRequired',
    )
  })

  it('accepts an empty list and exactly 64 mappings, rejects 65', () => {
    expect(validateAuthAttributeMappings([], 'oidc').valid).toBe(true)
    const mappings = Array.from({ length: AUTH_ATTRIBUTE_MAX_MAPPINGS }, (_, i) =>
      mapping(`custom.field_${i}`),
    )
    expect(validateAuthAttributeMappings(mappings, 'oidc').valid).toBe(true)
    expect(
      validateAuthAttributeMappings([...mappings, mapping('custom.extra')], 'oidc'),
    ).toMatchObject({ valid: false, limitReached: true })
  })

  it('clones the mapping input contract and drops server-owned field metadata', () => {
    const original = {
      ...mapping('external.account_age_days'),
      type: 'integer',
      required_scopes: ['profile'],
      optional: true,
      user_editable: false,
      allowed_operators: ['eq'],
      semantic_format: 'integer',
      dependency_fingerprint: 'fingerprint',
      trust_pair: { peer_field_id: 'external.email' },
    } as AuthAttributeMapping
    const clone = cloneAuthAttributeMappings([original])[0]!
    expect(clone).not.toHaveProperty('allowed_operators')
    expect(clone).not.toHaveProperty('semantic_format')
    expect(clone).not.toHaveProperty('dependency_fingerprint')
    expect(clone).not.toHaveProperty('trust_pair')
    expect(clone.required_scopes).toEqual(['profile'])
    clone.required_scopes?.push('email')
    expect(original.required_scopes).toEqual(['profile'])
    const minimal = cloneAuthAttributeMappings([{ ...original, required_scopes: undefined }])[0]!
    expect(minimal).toStrictEqual({
      field_id: 'external.account_age_days',
      label: 'Department',
      type: 'integer',
      source: 'oidc_id_token',
      pointer: '/department',
      optional: true,
      user_editable: false,
    })
  })
})

describe('attribute catalog source isolation', () => {
  const base = {
    instance: 'instance-a',
    administratorSession: 'session-a',
    revision: 7,
  }
  const externalSource = externalCatalogSourceKey({
    ...base,
    connectionId: 'connection-a',
    purpose: 'login',
  })
  const catalog = {
    revision: 7,
    purpose: 'login' as const,
    fields: [],
    operators: {},
  }

  it('separates resource, purpose, revision and candidate fingerprints', () => {
    const signup = externalCatalogSourceKey({
      ...base,
      connectionId: 'connection-a',
      purpose: 'signup',
    })
    const changed = externalCatalogSourceKey({
      ...base,
      connectionId: 'connection-a',
      purpose: 'login',
      changes: { display_name: 'Next' },
    })
    expect(authAttributeCatalogCacheKey(externalSource)).not.toBe(
      authAttributeCatalogCacheKey(signup),
    )
    expect(authAttributeCatalogCacheKey(externalSource)).not.toBe(
      authAttributeCatalogCacheKey(changed),
    )
    expect(authCatalogCandidateFingerprint({ changes: { display_name: 'Next' } })).not.toBe(
      authCatalogCandidateFingerprint(),
    )
  })

  it('keeps local signup tied to requirements and rejects other purposes', () => {
    const local = localCatalogSourceKey({ ...base, signupRequirements: ['email'] })
    const other = localCatalogSourceKey({
      ...base,
      signupRequirements: ['email', 'local_password'],
    })
    expect(local.purpose).toBe('signup')
    expect(local.resource).toEqual({ kind: 'local-account' })
    expect(authSignupRequirementsFingerprint(['email'])).not.toBe(
      authSignupRequirementsFingerprint(['email', 'local_password']),
    )
    expect(authAttributeCatalogCacheKey(local)).not.toBe(authAttributeCatalogCacheKey(other))
    const cache = new AuthAttributeCatalogCache()
    cache.set(local, { ...catalog, purpose: 'signup' })
    expect(cache.get(local)).toEqual({ ...catalog, purpose: 'signup' })
    expect(
      cache.get(localCatalogSourceKey({ ...base, signupRequirements: ['local_password'] })),
    ).toBeUndefined()
    expect(() =>
      cache.set({ ...local, purpose: 'login' }, { ...catalog, purpose: 'login' }),
    ).toThrow('Local-account attribute catalogs only support signup purpose')
  })

  it('invalidates all candidate fingerprints for one catalog identity', () => {
    const cache = new AuthAttributeCatalogCache()
    cache.set(externalSource, catalog)
    cache.set(
      externalCatalogSourceKey({
        ...base,
        connectionId: 'connection-a',
        purpose: 'login',
        changes: { display_name: 'Next' },
      }),
      catalog,
    )
    cache.set(
      externalCatalogSourceKey({
        instance: 'instance-b',
        administratorSession: 'session-a',
        connectionId: 'connection-a',
        revision: 7,
        purpose: 'login',
      }),
      catalog,
    )
    cache.invalidate({
      instance: 'instance-a',
      administratorSession: 'session-a',
      resource: { kind: 'external', id: 'connection-a' },
      revision: 7,
      purpose: 'login',
    })
    expect(cache.get(externalSource)).toBeUndefined()
    expect(
      cache.get({
        instance: 'instance-b',
        administratorSession: 'session-a',
        resource: { kind: 'external', id: 'connection-a' },
        revision: 7,
        purpose: 'login',
        candidateFingerprint: authCatalogCandidateFingerprint(),
      }),
    ).toEqual(catalog)
  })
})
