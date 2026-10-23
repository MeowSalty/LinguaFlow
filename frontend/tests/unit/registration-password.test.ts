import { describe, expect, it } from 'vitest'
import { validateNewPassword } from '../../src/utils/password'

describe('shared B5 new password boundaries', () => {
  it('counts Unicode code points while retaining whitespace', () => {
    expect(validateNewPassword('😀'.repeat(7))).toBe('tooShort')
    expect(validateNewPassword('😀'.repeat(8))).toBeNull()
    expect(validateNewPassword(' 123456 ')).toBeNull()
    expect(validateNewPassword(' '.repeat(8))).toBeNull()
  })

  it('enforces bcrypt 72 UTF-8 bytes for ASCII and multibyte passwords', () => {
    expect(validateNewPassword('a'.repeat(72))).toBeNull()
    expect(validateNewPassword('a'.repeat(73))).toBe('tooLong')
    expect(validateNewPassword('😀'.repeat(18))).toBeNull()
    expect(validateNewPassword('😀'.repeat(18) + 'a')).toBe('tooLong')
    expect(validateNewPassword('密'.repeat(24))).toBeNull()
    expect(validateNewPassword('密'.repeat(25))).toBe('tooLong')
  })
})
