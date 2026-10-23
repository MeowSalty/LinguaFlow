export type EmailIssue = 'required' | 'invalid'

/** Registration email is profile data; normalization never asserts ownership. */
export const normalizeEmail = (value: string): string => value.trim().toLowerCase()

export const validateEmail = (value: string, required = false): EmailIssue | null => {
  const normalized = normalizeEmail(value)
  if (!normalized) return required ? 'required' : null
  // A single plain address, excluding display names, quoted local parts and lists.
  if (!/^[^\s@<>(),;:"\\]+@[^\s@<>(),;:"\\]+\.[^\s@<>(),;:"\\]+$/u.test(normalized))
    return 'invalid'
  return null
}
