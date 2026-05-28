import { defineStore } from 'pinia'
import { ref } from 'vue'

import { type ApiSchemas, fetchStatsSummary, fetchActivity } from '@/api/client'

type UsageStats = ApiSchemas['UsageStats']
type Activity = ApiSchemas['Activity']

export const useStatsStore = defineStore('stats', () => {
  const stats = ref<UsageStats | null>(null)
  const activities = ref<Activity[]>([])
  const nextCursor = ref<string | undefined>(undefined)

  const statsLoading = ref(false)
  const activitiesLoading = ref(false)

  const statsError = ref<string | null>(null)
  const activitiesError = ref<string | null>(null)

  const loadStats = async (): Promise<void> => {
    statsLoading.value = true
    statsError.value = null

    try {
      stats.value = await fetchStatsSummary()
    } catch (error) {
      statsError.value = error instanceof Error ? error.message : '加载统计失败'
    } finally {
      statsLoading.value = false
    }
  }

  const loadActivities = async (reset = false): Promise<void> => {
    activitiesLoading.value = true
    activitiesError.value = null

    try {
      const cursor = reset ? undefined : nextCursor.value
      const response = await fetchActivity({ cursor, limit: 20 })

      if (reset) {
        activities.value = response.items
      } else {
        activities.value.push(...response.items)
      }

      nextCursor.value = response.next_cursor
    } catch (error) {
      activitiesError.value = error instanceof Error ? error.message : '加载活动失败'
    } finally {
      activitiesLoading.value = false
    }
  }

  const hasMoreActivities = computed(() => Boolean(nextCursor.value))

  const loadAll = async (): Promise<void> => {
    await Promise.all([loadStats(), loadActivities(true)])
  }

  return {
    stats,
    activities,
    statsLoading,
    activitiesLoading,
    statsError,
    activitiesError,
    hasMoreActivities,
    loadStats,
    loadActivities,
    loadAll,
  }
})
