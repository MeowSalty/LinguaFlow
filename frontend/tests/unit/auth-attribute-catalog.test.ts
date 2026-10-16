import { describe, expect, it } from 'vitest'
import {
  AUTH_ATTRIBUTE_TYPES,
  attributeSourcesForKind,
  cloneAuthAttributeFields,
  createAuthAttributeFieldDraft,
  isDerivedAttributeField,
  validateAuthAttributeMappings,
  type AuthAttributeField,
} from '@/components/admin/authentication/auth-attribute-catalog'

const field = (field_id = 'custom.department'): AuthAttributeField => ({
  ...createAuthAttributeFieldDraft('oidc'),
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
    expect(createAuthAttributeFieldDraft('generic_oauth2').source).toBe('oauth2_profile')
    expect(createAuthAttributeFieldDraft('oidc').source).toBe('oidc_id_token')
  })
  it('rejects empty required fields, duplicate ids and incompatible sources', () => {
    expect(
      validateAuthAttributeMappings([createAuthAttributeFieldDraft('oidc')], 'oidc').valid,
    ).toBe(false)
    expect(validateAuthAttributeMappings([field(), field()], 'oidc').rows).toEqual([
      { index: 1, code: 'fieldIdDuplicate' },
    ])
    expect(validateAuthAttributeMappings([field()], 'generic_oauth2').rows).toEqual([
      { index: 0, code: 'sourceNotAllowed' },
    ])
    expect(validateAuthAttributeMappings([{ ...field(), pointer: '' }], 'oidc').rows[0]?.code).toBe(
      'pointerRequired',
    )
    expect(validateAuthAttributeMappings([{ ...field(), label: '' }], 'oidc').rows[0]?.code).toBe(
      'labelRequired',
    )
  })
  it('accepts an empty list and exactly 64 fields, rejects 65', () => {
    expect(validateAuthAttributeMappings([], 'oidc').valid).toBe(true)
    const fields = Array.from({ length: 64 }, (_, i) => field(`custom.field_${i}`))
    expect(validateAuthAttributeMappings(fields, 'oidc').valid).toBe(true)
    expect(validateAuthAttributeMappings([...fields, field('custom.extra')], 'oidc')).toMatchObject(
      { valid: false, limitReached: true },
    )
  })
  it('identifies server-derived rows and preserves all metadata when cloning', () => {
    const original: AuthAttributeField = {
      ...field('external.account_age_days'),
      type: 'integer',
      source: 'derived',
      pointer: 'external.account_created_at',
      derived_from: ['external.account_created_at'],
      required_scopes: ['profile'],
      optional: true,
      user_editable: false,
    }
    const clone = cloneAuthAttributeFields([original])[0]!
    expect(isDerivedAttributeField(clone)).toBe(true)
    expect(validateAuthAttributeMappings([clone], 'generic_oauth2').valid).toBe(true)
    expect(clone).toEqual(original)
    clone.required_scopes?.push('email')
    clone.derived_from?.push('other')
    expect(original.required_scopes).toEqual(['profile'])
    expect(original.derived_from).toEqual(['external.account_created_at'])
  })

  it('does not add optional metadata to server-provided rows', () => {
    const original: AuthAttributeField = {
      field_id: 'external.age',
      label: 'Age',
      type: 'integer',
      source: 'derived',
      pointer: 'external.created_at',
    }
    expect(cloneAuthAttributeFields([original])).toStrictEqual([original])
  })
})
