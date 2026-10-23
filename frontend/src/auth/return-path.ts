/** Accept only an application path, including after percent decoding. */
const unsafeCharacters = (value: string): boolean =>
  Array.from(value).some(
    (character) =>
      character === '\\' || character.charCodeAt(0) <= 0x20 || character.charCodeAt(0) === 0x7f,
  )
export function safeReturnPath(value: unknown, fallback = '/'): string {
  if (typeof value !== 'string' || value.length > 2048) return fallback
  let decoded = value
  try {
    for (let i = 0; i < 4; i++) {
      if (!decoded.startsWith('/') || decoded.startsWith('//') || unsafeCharacters(decoded))
        return fallback
      const next = decodeURIComponent(decoded)
      if (next === decoded) break
      decoded = next
    }
  } catch {
    return fallback
  }
  if (!decoded.startsWith('/') || decoded.startsWith('//') || unsafeCharacters(decoded))
    return fallback
  const pathname = decoded.split(/[?#]/)[0]
  if (['/login', '/register', '/auth/complete'].includes(pathname ?? '')) return fallback
  return value
}
