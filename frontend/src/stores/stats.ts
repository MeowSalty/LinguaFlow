import { defineStore } from 'pinia'
import { computed, onScopeDispose, ref, shallowRef } from 'vue'
import { type ApiSchemas, fetchStatsSummary, fetchActivity } from '@/api/client'
import { captureSession, isSessionCurrent, onSessionChange } from '@/api/session-context'
import { isAccessDenied } from '@/api/utils'
import { onOrganizationInvalidated } from '@/utils/organization-scope'
import { t } from '@/i18n'

export const useStatsStore = defineStore('stats', () => {
  const stats = shallowRef<ApiSchemas['UsageStats'] | null>(null)
  const activities = shallowRef<ApiSchemas['Activity'][]>([])
  const nextCursor = ref<string | undefined>()
  const statsLoading = ref(false)
  const activitiesLoading = ref(false)
  const statsError = ref<string | null>(null)
  const activitiesError = ref<string | null>(null)
  const statsUpdatedAt = ref<string | null>(null)
  const activityOrgId = ref<number | null>(null)
  let statsRequest: Promise<void> | null = null
  let activityRequest: Promise<void> | null = null
  let activityGeneration = 0
  let statsGeneration = 0
  const revision = ref(0)
  const invalidate = (publish = true): void => {
    statsGeneration++
    statsRequest = null
    statsLoading.value = false
    if (publish) revision.value++
  }
  const publishInvalidation = (): void => {
    revision.value++
  }

  const clearActivities = (): void => {
    activityGeneration++
    activityRequest = null
    activities.value = []
    nextCursor.value = undefined
    activitiesError.value = null
    activitiesLoading.value = false
  }
  const reset = (): void => {
    statsGeneration++
    statsRequest = null
    stats.value = null
    statsLoading.value = false
    statsError.value = null
    statsUpdatedAt.value = null
    activityOrgId.value = null
    clearActivities()
  }
  onScopeDispose(onSessionChange(reset))
  onOrganizationInvalidated((orgId) => {
    if (activityOrgId.value === orgId) clearActivities()
  })

  const loadStats = (): Promise<void> => {
    if (statsRequest) return statsRequest
    const session = captureSession()
    const generation = statsGeneration
    const current = () => isSessionCurrent(session) && generation === statsGeneration
    statsLoading.value = true
    statsError.value = null
    const work = async (): Promise<void> => {
      try {
        // Usage statistics do not support organization filtering.
        const response = await fetchStatsSummary()
        if (current()) {
          stats.value = response
          statsUpdatedAt.value = new Date().toISOString()
        }
      } catch (error) {
        if (!current()) return
        if (isAccessDenied(error)) stats.value = null
        statsError.value = error instanceof Error ? error.message : t('api.errors.loadStatsFailed')
      } finally {
        if (current()) {
          statsLoading.value = false
          statsRequest = null
        }
      }
    }
    statsRequest = work()
    return statsRequest
  }

  const loadActivities = (resetPage = false): Promise<void> => {
    if (activityRequest) return activityRequest
    const session = captureSession()
    const generation = activityGeneration
    const current = () => isSessionCurrent(session) && generation === activityGeneration
    activitiesLoading.value = true
    activitiesError.value = null
    const work = async (): Promise<void> => {
      try {
        const response = await fetchActivity({
          cursor: resetPage ? undefined : nextCursor.value,
          limit: 20,
          ...(activityOrgId.value === null ? {} : { org_id: activityOrgId.value }),
        })
        if (!current()) return
        const items = resetPage ? response.items : [...activities.value, ...response.items]
        activities.value = [...new Map(items.map((item) => [item.id, item])).values()]
        nextCursor.value = response.next_cursor
      } catch (error) {
        if (!current()) return
        if (isAccessDenied(error)) {
          activities.value = []
          nextCursor.value = undefined
        }
        activitiesError.value =
          error instanceof Error ? error.message : t('api.errors.loadActivityFailed')
      } finally {
        if (current()) {
          activitiesLoading.value = false
          activityRequest = null
        }
      }
    }
    activityRequest = work()
    return activityRequest
  }
  const setActivityOrganization = (orgId: number | null): void => {
    if (activityOrgId.value === orgId) return
    clearActivities()
    activityOrgId.value = orgId
  }
  const loadAll = async (): Promise<void> => {
    await Promise.all([loadStats(), loadActivities(true)])
  }
  const hasMoreActivities = computed(() => Boolean(nextCursor.value))
  return {
    stats,
    activities,
    statsLoading,
    activitiesLoading,
    statsError,
    activitiesError,
    statsUpdatedAt,
    activityOrgId,
    hasMoreActivities,
    loadStats,
    loadActivities,
    loadAll,
    setActivityOrganization,
    reset,
    invalidate,
    publishInvalidation,
    revision,
  }
})
