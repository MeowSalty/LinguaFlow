import { countUnicodeCodePoints } from './unicode'

export type PasswordIssue = 'tooShort' | 'tooLong'

/** Match the server's code-point minimum and bcrypt UTF-8 byte ceiling without trimming. */
export const validateNewPassword = (password: string): PasswordIssue | null => {
  if (countUnicodeCodePoints(password) < 8) return 'tooShort'
  if (new TextEncoder().encode(password).length > 72) return 'tooLong'
  return null
}
