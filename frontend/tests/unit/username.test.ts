import { describe, expect, it } from 'vitest'

import { normalizeUsername, validateUsername } from '@/utils/username'

const ADMIN_RESERVED = ['admin', 'administrator', 'root']
const SYSTEM_RESERVED = [
  'local',
  'system',
  'anonymous',
  'api',
  'auth',
  'login',
  'logout',
  'register',
  'signup',
  'settings',
  'users',
  'support',
  'security',
]

describe('normalizeUsername', () => {
  it('trims only leading and trailing plain spaces', () => {
    expect(normalizeUsername('  alice  ')).toBe('alice')
    expect(normalizeUsername('   ')).toBe('')
    expect(normalizeUsername('al ice')).toBe('al ice')
    expect(normalizeUsername('\talice\t')).toBe('\talice\t')
  })

  it('folds ASCII A-Z without touching anything else', () => {
    expect(normalizeUsername('Alice-B_2000')).toBe('alice-b_2000')
    expect(normalizeUsername('ADMIN')).toBe('admin')
  })

  it('never folds non-ASCII characters into ASCII', () => {
    const kelvinSign = '\u212A'
    const dottedCapitalI = '\u0130'
    expect(normalizeUsername(kelvinSign)).toBe(kelvinSign)
    expect(normalizeUsername(`Ab${kelvinSign}`)).toBe(`ab${kelvinSign}`)
    expect(normalizeUsername(dottedCapitalI)).toBe(dottedCapitalI)
  })

  it('keeps illegal characters instead of replacing them', () => {
    expect(normalizeUsername('a b!c')).toBe('a b!c')
    expect(normalizeUsername('a\tb')).toBe('a\tb')
  })
})

describe('validateUsername input rejection', () => {
  it('rejects non-ASCII and control characters as input', () => {
    expect(validateUsername('\u212Aelvin')).toBe('input')
    expect(validateUsername('alice\u0130')).toBe('input')
    expect(validateUsername('用户名')).toBe('input')
    expect(validateUsername('alice\u0000')).toBe('input')
    expect(validateUsername('alice\u007F')).toBe('input')
    expect(validateUsername('alice\t')).toBe('input')
    expect(validateUsername('alice\n')).toBe('input')
  })

  it('rejects input longer than 128 bytes but accepts the boundary', () => {
    expect(validateUsername('a'.repeat(128))).not.toBe('input')
    expect(validateUsername('a'.repeat(129))).toBe('input')
  })

  it('checks input before format so illegal characters never normalize away', () => {
    expect(validateUsername(' \u212A ')).toBe('input')
    expect(validateUsername('\u212A'.repeat(3))).toBe('input')
  })
})

describe('validateUsername format', () => {
  it('accepts canonical usernames and normalizes case and outer spaces', () => {
    expect(validateUsername('alice')).toBeNull()
    expect(validateUsername('Alice-2000')).toBeNull()
    expect(validateUsername('  alice_99  ')).toBeNull()
    expect(validateUsername('a_b-c')).toBeNull()
  })

  it('enforces the 3-32 length window on the normalized value', () => {
    expect(validateUsername('ab')).toBe('format')
    expect(validateUsername('abc')).toBeNull()
    expect(validateUsername('a'.repeat(32))).toBeNull()
    expect(validateUsername('a'.repeat(33))).toBe('format')
    expect(validateUsername('ab ')).toBe('format')
  })

  it('requires a leading letter and a trailing letter or digit', () => {
    expect(validateUsername('1alice')).toBe('format')
    expect(validateUsername('-alice')).toBe('format')
    expect(validateUsername('_alice')).toBe('format')
    expect(validateUsername('alice-')).toBe('format')
    expect(validateUsername('alice_')).toBe('format')
  })

  it('rejects adjacent separators and characters outside the alphabet', () => {
    expect(validateUsername('alice--bob')).toBe('format')
    expect(validateUsername('alice__bob')).toBe('format')
    expect(validateUsername('alice-_bob')).toBe('format')
    expect(validateUsername('alice.-bob')).toBe('format')
    expect(validateUsername('alice bob')).toBe('format')
    expect(validateUsername('alice.2000')).toBe('format')
    expect(validateUsername('alice@example')).toBe('format')
  })
})

describe('validateUsername reserved names', () => {
  it('reserves admin, administrator and root for non-admin purposes', () => {
    for (const name of ADMIN_RESERVED) {
      expect(validateUsername(name)).toBe('reserved')
      expect(validateUsername(name, 'lookup')).toBeNull()
    }
    expect(validateUsername('ADMIN')).toBe('reserved')
    expect(validateUsername('  root  ')).toBe('reserved')
  })

  it('allows admin, administrator and root when an admin creates an admin role', () => {
    for (const name of ADMIN_RESERVED) {
      expect(validateUsername(name, 'admin')).toBeNull()
    }
  })

  it('rejects every system reserved name for signup and admin creation', () => {
    for (const name of SYSTEM_RESERVED) {
      expect(validateUsername(name)).toBe('reserved')
      expect(validateUsername(name, 'admin')).toBe('reserved')
    }
  })

  it('lets lookup use every reserved name but still enforces input and format', () => {
    for (const name of [...ADMIN_RESERVED, ...SYSTEM_RESERVED]) {
      expect(validateUsername(name, 'lookup')).toBeNull()
    }
    expect(validateUsername('ab', 'lookup')).toBe('format')
    expect(validateUsername('admin!', 'lookup')).toBe('format')
    expect(validateUsername('root\u212A', 'lookup')).toBe('input')
  })
})
