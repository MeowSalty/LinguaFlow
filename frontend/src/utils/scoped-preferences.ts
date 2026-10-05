export type DefaultTaskState = 'active' | 'terminal' | 'all'
export interface RecentProjectVisit {
  project_id: number
  last_opened_at: string
}
export interface ScopedPreferences {
  version: 1
  defaultTaskState: DefaultTaskState
  retainTerminal: boolean
  quickTranslatePlanId: number | null
  hiddenTerminalKeys: string[]
  recentProjects: RecentProjectVisit[]
  selectedOrgId: number | null
}

export const defaultPreferences = (): ScopedPreferences => ({
  version: 1,
  defaultTaskState: 'active',
  retainTerminal: true,
  quickTranslatePlanId: null,
  hiddenTerminalKeys: [],
  recentProjects: [],
  selectedOrgId: null,
})
const positiveId = (value: unknown): value is number =>
  typeof value === 'number' && Number.isSafeInteger(value) && value > 0
const memory = new Map<string, string>()
const keyFor = (scope: string) => `linguaflow.preferences.v1:${encodeURIComponent(scope)}`

export const sanitizePreferences = (value: unknown): ScopedPreferences => {
  const result = defaultPreferences()
  if (!value || typeof value !== 'object' || !('version' in value) || value.version !== 1)
    return result
  const data = value as Partial<ScopedPreferences>
  if (
    data.defaultTaskState === 'active' ||
    data.defaultTaskState === 'terminal' ||
    data.defaultTaskState === 'all'
  )
    result.defaultTaskState = data.defaultTaskState
  if (typeof data.retainTerminal === 'boolean') result.retainTerminal = data.retainTerminal
  if (
    typeof data.quickTranslatePlanId === 'number' &&
    Number.isSafeInteger(data.quickTranslatePlanId) &&
    data.quickTranslatePlanId !== 0
  )
    result.quickTranslatePlanId = data.quickTranslatePlanId
  if (Array.isArray(data.hiddenTerminalKeys))
    result.hiddenTerminalKeys = [
      ...new Set(
        data.hiddenTerminalKeys.filter(
          (key) =>
            typeof key === 'string' &&
            /^(translation|glossary_sync|storage):[1-9][0-9]*$/.test(key),
        ),
      ),
    ].slice(-500)
  if (Array.isArray(data.recentProjects)) {
    const seen = new Set<number>()
    result.recentProjects = data.recentProjects
      .filter((item): item is RecentProjectVisit => {
        if (
          !item ||
          typeof item !== 'object' ||
          !positiveId(item.project_id) ||
          typeof item.last_opened_at !== 'string' ||
          !Number.isFinite(Date.parse(item.last_opened_at)) ||
          seen.has(item.project_id)
        )
          return false
        seen.add(item.project_id)
        return true
      })
      .map((item) => ({ project_id: item.project_id, last_opened_at: item.last_opened_at }))
      .sort((a, b) => Date.parse(b.last_opened_at) - Date.parse(a.last_opened_at))
      .slice(0, 20)
  }
  if (positiveId(data.selectedOrgId)) result.selectedOrgId = data.selectedOrgId
  return result
}

export const readScopedPreferences = (scope: string | null): ScopedPreferences => {
  if (!scope) return defaultPreferences()
  const key = keyFor(scope)
  let stored = memory.get(key)
  if (!stored) {
    try {
      stored = globalThis.localStorage?.getItem(key) ?? undefined
    } catch {
      /* Use memory when storage is disabled. */
    }
  }
  try {
    return sanitizePreferences(stored ? JSON.parse(stored) : null)
  } catch {
    return defaultPreferences()
  }
}

export const writeScopedPreferences = (scope: string | null, value: ScopedPreferences): void => {
  if (!scope) return
  const key = keyFor(scope)
  const serialized = JSON.stringify(sanitizePreferences(value))
  memory.delete(key)
  memory.set(key, serialized)
  while (memory.size > 20) memory.delete(memory.keys().next().value!)
  try {
    globalThis.localStorage?.setItem(key, serialized)
  } catch {
    /* The current session remains usable. */
  }
}
