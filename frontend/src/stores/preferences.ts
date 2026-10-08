import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
import { getSessionScope, sessionGeneration } from '@/api/session-context'
import {
  readScopedPreferences,
  writeScopedPreferences,
  type DefaultTaskState,
  type RecentProjectVisit,
} from '@/utils/scoped-preferences'

export const usePreferencesStore = defineStore('preferences', () => {
  const defaultTaskState = ref<DefaultTaskState>('active')
  const retainTerminal = ref(true)
  const quickTranslatePlanId = ref<number | null>(null)
  const hiddenTerminalKeys = ref<string[]>([])
  const recentProjects = ref<RecentProjectVisit[]>([])
  const selectedOrgId = ref<number | null>(null)
  let loading = false

  watch(
    sessionGeneration,
    () => {
      loading = true
      const data = readScopedPreferences(getSessionScope())
      defaultTaskState.value = data.defaultTaskState
      retainTerminal.value = data.retainTerminal
      quickTranslatePlanId.value = data.quickTranslatePlanId
      hiddenTerminalKeys.value = data.hiddenTerminalKeys
      recentProjects.value = data.recentProjects
      selectedOrgId.value = data.selectedOrgId
      loading = false
    },
    { immediate: true, flush: 'sync' },
  )

  watch(
    [
      defaultTaskState,
      retainTerminal,
      quickTranslatePlanId,
      hiddenTerminalKeys,
      recentProjects,
      selectedOrgId,
    ],
    () => {
      if (loading) return
      writeScopedPreferences(getSessionScope(), {
        version: 1,
        defaultTaskState: defaultTaskState.value,
        retainTerminal: retainTerminal.value,
        quickTranslatePlanId: quickTranslatePlanId.value,
        hiddenTerminalKeys: hiddenTerminalKeys.value,
        recentProjects: recentProjects.value,
        selectedOrgId: selectedOrgId.value,
      })
    },
    { deep: true, flush: 'sync' },
  )

  const hideTerminal = (key: string): void => {
    if (!/^(translation|glossary_sync|storage):[1-9][0-9]*$/.test(key)) return
    hiddenTerminalKeys.value = [
      ...hiddenTerminalKeys.value.filter((item) => item !== key),
      key,
    ].slice(-500)
  }
  const clearHiddenTerminals = (): void => {
    hiddenTerminalKeys.value = []
  }
  const removeHiddenTerminals = (keys: string[]): void => {
    const removed = new Set(keys)
    hiddenTerminalKeys.value = hiddenTerminalKeys.value.filter((key) => !removed.has(key))
  }
  const recordVisit = (projectId: number): void => {
    if (!getSessionScope() || !Number.isSafeInteger(projectId) || projectId < 1) return
    recentProjects.value = [
      { project_id: projectId, last_opened_at: new Date().toISOString() },
      ...recentProjects.value.filter((item) => item.project_id !== projectId),
    ].slice(0, 20)
  }
  const pruneRecentProjects = (authorizedIds: number[]): void => {
    const allowed = new Set(authorizedIds)
    recentProjects.value = recentProjects.value.filter((item) => allowed.has(item.project_id))
  }
  const setSelectedOrg = (orgId: number | null): void => {
    selectedOrgId.value = orgId !== null && Number.isSafeInteger(orgId) && orgId > 0 ? orgId : null
  }
  return {
    defaultTaskState,
    retainTerminal,
    quickTranslatePlanId,
    hiddenTerminalKeys,
    recentProjects,
    selectedOrgId,
    hideTerminal,
    clearHiddenTerminals,
    removeHiddenTerminals,
    recordVisit,
    pruneRecentProjects,
    setSelectedOrg,
  }
})
