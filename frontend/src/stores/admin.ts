import { defineStore } from 'pinia'
import { ref, computed } from 'vue'

import { type ApiSchemas } from '@/api/client'
import {
  fetchAdminStats,
  fetchAdminUsers,
  createAdminUser,
  updateAdminUser,
  disableAdminUser,
  resetAdminUserPassword,
  fetchAdminAuditLogs,
  fetchAdminSettings,
  updateAdminSettings,
} from '@/api/admin'
import { t } from '@/i18n'

type SystemStats = ApiSchemas['SystemStats']
type User = ApiSchemas['User']
type Activity = ApiSchemas['Activity']

export const useAdminStore = defineStore('admin', () => {
  const stats = ref<SystemStats | null>(null)
  const statsLoading = ref(false)
  const statsError = ref<string | null>(null)

  const users = ref<User[]>([])
  const usersTotal = ref(0)
  const usersLoading = ref(false)
  const usersError = ref<string | null>(null)
  const userSearchQuery = ref('')
  const userRoleFilter = ref<string>('all')
  const userActiveFilter = ref<string>('all')

  const auditLogs = ref<Activity[]>([])
  const auditLogsTotal = ref(0)
  const auditLogsLoading = ref(false)
  const auditLogsError = ref<string | null>(null)

  const settings = ref<Record<string, string>>({})
  const settingsLoading = ref(false)
  const settingsError = ref<string | null>(null)
  const settingsSaving = ref(false)

  const creatingUser = ref(false)
  const updatingUser = ref(false)
  const disablingUserIds = ref<number[]>([])
  const resettingPasswordUserIds = ref<number[]>([])

  const filteredUsers = computed(() => {
    let result = users.value

    if (userSearchQuery.value.trim()) {
      const query = userSearchQuery.value.toLowerCase()
      result = result.filter(
        (u) =>
          u.username.toLowerCase().includes(query) ||
          u.email.toLowerCase().includes(query) ||
          (u.display_name?.toLowerCase().includes(query) ?? false),
      )
    }

    if (userRoleFilter.value !== 'all') {
      result = result.filter((u) => u.role === userRoleFilter.value)
    }

    if (userActiveFilter.value === 'active') {
      result = result.filter((u) => u.active)
    } else if (userActiveFilter.value === 'inactive') {
      result = result.filter((u) => !u.active)
    }

    return result
  })

  const loadStats = async (): Promise<void> => {
    statsLoading.value = true
    statsError.value = null

    try {
      stats.value = await fetchAdminStats()
    } catch (error) {
      statsError.value =
        error instanceof Error ? error.message : t('api.errors.fetchAdminStatsFailed')
    } finally {
      statsLoading.value = false
    }
  }

  const loadUsers = async (): Promise<void> => {
    usersLoading.value = true
    usersError.value = null

    try {
      const params: Record<string, unknown> = { limit: 100 }
      if (userRoleFilter.value !== 'all') {
        params.role = userRoleFilter.value
      }
      if (userActiveFilter.value === 'active') {
        params.active = true
      } else if (userActiveFilter.value === 'inactive') {
        params.active = false
      }

      const response = await fetchAdminUsers(params)
      users.value = response.items
      usersTotal.value = response.total
    } catch (error) {
      usersError.value =
        error instanceof Error ? error.message : t('api.errors.fetchAdminUsersFailed')
    } finally {
      usersLoading.value = false
    }
  }

  const createUser = async (payload: ApiSchemas['AdminCreateUserRequest']): Promise<User> => {
    creatingUser.value = true

    try {
      const user = await createAdminUser(payload)
      users.value.unshift(user)
      usersTotal.value++
      return user
    } finally {
      creatingUser.value = false
    }
  }

  const updateUser = async (
    userId: number,
    payload: ApiSchemas['AdminUpdateUserRequest'],
  ): Promise<User> => {
    updatingUser.value = true

    try {
      const updated = await updateAdminUser(userId, payload)
      const index = users.value.findIndex((u) => u.id === userId)
      if (index !== -1) {
        users.value[index] = updated
      }
      return updated
    } finally {
      updatingUser.value = false
    }
  }

  const disableUser = async (userId: number): Promise<void> => {
    disablingUserIds.value.push(userId)

    try {
      await disableAdminUser(userId)
      const index = users.value.findIndex((u) => u.id === userId)
      const user = users.value[index]
      if (user) {
        users.value[index] = { ...user, active: false }
      }
    } finally {
      disablingUserIds.value = disablingUserIds.value.filter((id) => id !== userId)
    }
  }

  const resetPassword = async (
    userId: number,
    payload: ApiSchemas['AdminResetPasswordRequest'],
  ): Promise<void> => {
    resettingPasswordUserIds.value.push(userId)

    try {
      await resetAdminUserPassword(userId, payload)
    } finally {
      resettingPasswordUserIds.value = resettingPasswordUserIds.value.filter((id) => id !== userId)
    }
  }

  const loadAuditLogs = async (reset = false): Promise<void> => {
    auditLogsLoading.value = true
    auditLogsError.value = null

    try {
      const response = await fetchAdminAuditLogs({ limit: 50 })
      if (reset) {
        auditLogs.value = response.items
      } else {
        auditLogs.value.push(...response.items)
      }
      auditLogsTotal.value = response.total
    } catch (error) {
      auditLogsError.value =
        error instanceof Error ? error.message : t('api.errors.fetchAdminAuditLogsFailed')
    } finally {
      auditLogsLoading.value = false
    }
  }

  const loadSettings = async (): Promise<void> => {
    settingsLoading.value = true
    settingsError.value = null

    try {
      const response = await fetchAdminSettings()
      settings.value = response.settings ?? {}
    } catch (error) {
      settingsError.value =
        error instanceof Error ? error.message : t('api.errors.fetchAdminSettingsFailed')
    } finally {
      settingsLoading.value = false
    }
  }

  const saveSettings = async (newSettings: Record<string, string>): Promise<void> => {
    settingsSaving.value = true

    try {
      const response = await updateAdminSettings({ settings: newSettings })
      settings.value = response.settings ?? {}
    } finally {
      settingsSaving.value = false
    }
  }

  const loadAll = async (): Promise<void> => {
    await Promise.all([loadStats(), loadUsers()])
  }

  return {
    stats,
    statsLoading,
    statsError,
    users,
    usersTotal,
    usersLoading,
    usersError,
    userSearchQuery,
    userRoleFilter,
    userActiveFilter,
    filteredUsers,
    auditLogs,
    auditLogsTotal,
    auditLogsLoading,
    auditLogsError,
    settings,
    settingsLoading,
    settingsError,
    settingsSaving,
    creatingUser,
    updatingUser,
    disablingUserIds,
    resettingPasswordUserIds,
    loadStats,
    loadUsers,
    createUser,
    updateUser,
    disableUser,
    resetPassword,
    loadAuditLogs,
    loadSettings,
    saveSettings,
    loadAll,
  }
})
