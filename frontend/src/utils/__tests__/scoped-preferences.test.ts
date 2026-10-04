import { describe, expect, it, vi, afterEach } from 'vitest'
import {
  defaultPreferences,
  readScopedPreferences,
  sanitizePreferences,
  writeScopedPreferences,
} from '../scoped-preferences'

afterEach(() => vi.unstubAllGlobals())
describe('scoped preferences', () => {
  it('recovers from malformed, legacy, and unsupported records', () => {
    vi.stubGlobal('localStorage', { getItem: () => '{invalid' })
    expect(readScopedPreferences('broken')).toEqual(defaultPreferences())
    expect(sanitizePreferences({ version: 2, defaultTaskState: 'all' })).toEqual(
      defaultPreferences(),
    )
    expect(sanitizePreferences(null)).toEqual(defaultPreferences())
  })
  it('keeps account and instance partitions separate when storage is blocked', () => {
    vi.stubGlobal('localStorage', {
      getItem: () => {
        throw new Error('blocked')
      },
      setItem: () => {
        throw new Error('blocked')
      },
    })
    writeScopedPreferences('https://a/api|1', {
      ...defaultPreferences(),
      defaultTaskState: 'terminal',
    })
    writeScopedPreferences('https://a/api|2', { ...defaultPreferences(), retainTerminal: false })
    expect(readScopedPreferences('https://a/api|1').defaultTaskState).toBe('terminal')
    expect(readScopedPreferences('https://a/api|2').retainTerminal).toBe(false)
    expect(readScopedPreferences('https://b/api|1')).toEqual(defaultPreferences())
    expect(readScopedPreferences(null)).toEqual(defaultPreferences())
  })
  it('bounds history and reminders, deduplicates identity, and rejects unsafe IDs', () => {
    const result = sanitizePreferences({
      version: 1,
      hiddenTerminalKeys: [
        ...Array.from({ length: 520 }, (_, i) => `translation:${i + 1}`),
        'glossary_sync:1',
        'storage:1',
        'translation:0',
        'whatever:9',
      ],
      recentProjects: [
        ...Array.from({ length: 30 }, (_, i) => ({
          project_id: i + 1,
          last_opened_at: new Date(2026, 0, i + 1).toISOString(),
        })),
        { project_id: 1, last_opened_at: 'invalid' },
        { project_id: Number.MAX_SAFE_INTEGER + 1, last_opened_at: new Date().toISOString() },
      ],
      selectedOrgId: Number.MAX_SAFE_INTEGER + 1,
    })
    expect(result.recentProjects).toHaveLength(20)
    expect(result.recentProjects[0]?.project_id).toBe(30)
    expect(result.hiddenTerminalKeys).toHaveLength(500)
    expect(result.hiddenTerminalKeys).toContain('glossary_sync:1')
    expect(result.hiddenTerminalKeys).toContain('storage:1')
    expect(result.selectedOrgId).toBeNull()
  })
})
