import { describe, expect, it } from 'vitest'
import { validateNewPassword } from '../password'

describe('new password contract', () => {
  it('counts Unicode code points rather than UTF-16 code units', () => {
    expect(validateNewPassword('😀'.repeat(7))).toBe('tooShort')
    expect(validateNewPassword('😀'.repeat(8))).toBeNull()
  })
  it('enforces the UTF-8 ceiling for ASCII and multibyte input', () => {
    expect(validateNewPassword('a'.repeat(72))).toBeNull()
    expect(validateNewPassword('a'.repeat(73))).toBe('tooLong')
    expect(validateNewPassword('中'.repeat(24))).toBeNull()
    expect(validateNewPassword('中'.repeat(25))).toBe('tooLong')
    expect(validateNewPassword('😀'.repeat(18))).toBeNull()
    expect(validateNewPassword('😀'.repeat(19))).toBe('tooLong')
  })
  it('preserves whitespace and does not normalize combining characters', () => {
    expect(validateNewPassword(' 123456 ')).toBeNull()
    expect(validateNewPassword(' '.repeat(8))).toBeNull()
    expect(validateNewPassword('e\u0301'.repeat(4))).toBeNull()
  })
})
