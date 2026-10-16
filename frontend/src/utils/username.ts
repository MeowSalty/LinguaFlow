const USERNAME_PATTERN = /^[a-z][a-z0-9]*(?:[-_][a-z0-9]+)*$/
const ASCII_UPPER_PATTERN = /[A-Z]/g
const NON_ASCII_OR_CONTROL_PATTERN = /[^\x20-\x7E]/
const TRIM_SPACES_PATTERN = /^ +| +$/g
const encoder = new TextEncoder()

export type UsernamePurpose = 'signup' | 'admin' | 'lookup'
export type UsernameIssue = 'input' | 'format' | 'reserved'

const ADMIN_RESERVED_USERNAMES = new Set(['admin', 'administrator', 'root'])
const SYSTEM_RESERVED_USERNAMES = new Set([
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
])

/**
 * Normalize only the transformations the username contract allows.
 * Only ASCII A-Z is folded: Unicode case mapping must never turn non-ASCII
 * characters (such as KELVIN SIGN) into ASCII, and illegal characters stay in
 * place so validation rejects them instead of silently rewriting the input.
 */
export const normalizeUsername = (value: string): string =>
  value.replace(TRIM_SPACES_PATTERN, '').replace(ASCII_UPPER_PATTERN, (char) => char.toLowerCase())

const hasInvalidInput = (value: string): boolean =>
  NON_ASCII_OR_CONTROL_PATTERN.test(value) || encoder.encode(value).length > 128

const hasValidFormat = (value: string): boolean =>
  value.length >= 3 && value.length <= 32 && USERNAME_PATTERN.test(value)

/**
 * Validate raw username input without accepting Unicode folding or silent edits.
 * The admin purpose is reserved for an authorized admin creating an admin role.
 */
export const validateUsername = (
  value: string,
  purpose: UsernamePurpose = 'signup',
): UsernameIssue | null => {
  if (hasInvalidInput(value)) return 'input'

  const normalized = normalizeUsername(value)
  if (!hasValidFormat(normalized)) return 'format'
  if (purpose === 'lookup') return null

  if (SYSTEM_RESERVED_USERNAMES.has(normalized)) return 'reserved'
  if (ADMIN_RESERVED_USERNAMES.has(normalized) && purpose !== 'admin') return 'reserved'

  return null
}
