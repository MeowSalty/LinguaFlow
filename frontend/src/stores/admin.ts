import { defineStore } from 'pinia'
import { ref, computed, onScopeDispose } from 'vue'

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
import { captureSession, isSessionCurrent, onSessionChange } from '@/api/session-context'
import { ApiError } from '@/api/utils'
import {
  TaskHistoryApiError,
  taskHistoryErrorMessage,
  validRetentionPolicy,
} from '@/api/task-history'

type SystemStats = ApiSchemas['SystemStats']
type User = ApiSchemas['User']
type Activity = ApiSchemas['Activity']
type SystemSettings = ApiSchemas['SystemSettingsResponse']['settings']

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

  const settings = ref<SystemSettings | null>(null)
  const settingsLoading = ref(false)
  const settingsError = ref<string | null>(null)
  const settingsSaving = ref(false)
  const settingsSaveError = ref<string | null>(null)
  const settingsAccessDenied = ref(false)
  let settingsRequest = 0
  let latestPolicy: ApiSchemas['TaskRetentionPolicy'] | null = null
  const acceptRetentionPolicy = (policy: ApiSchemas['TaskRetentionPolicy']): void => {
    if (!validRetentionPolicy(policy) || (latestPolicy && policy.revision < latestPolicy.revision))
      return
    latestPolicy = { ...policy }
    if (settings.value) settings.value = { ...settings.value, task_retention: latestPolicy }
  }
  const acceptSettings = (value: SystemSettings): void => {
    if (!latestPolicy || value.task_retention.revision >= latestPolicy.revision)
      latestPolicy = { ...value.task_retention }
    settings.value = { ...value, task_retention: latestPolicy }
  }
  const clearSettings = (denied = false): void => {
    settingsRequest++
    latestPolicy = null
    settings.value = null
    settingsLoading.value = false
    settingsSaving.value = false
    settingsError.value = null
    settingsSaveError.value = null
    settingsAccessDenied.value = denied
  }

  onScopeDispose(onSessionChange(() => clearSettings()))

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

  const loadSettings = async (): Promise<boolean> => {
    if (settingsLoading.value || settingsSaving.value) return false
    const context = captureSession()
    const request = ++settingsRequest
    const current = () => request === settingsRequest && isSessionCurrent(context)
    settingsLoading.value = true
    settingsError.value = null
    settingsSaveError.value = null

    try {
      const response = await fetchAdminSettings()
      if (!current()) return false
      acceptSettings(response.settings)
      settingsAccessDenied.value = false
      settingsSaveError.value = null
      return true
    } catch (error) {
      if (!current()) return false
      const denied = error instanceof ApiError && [401, 403].includes(error.status ?? 0)
      if (denied) clearSettings(true)
      settingsError.value = denied
        ? t('configurationSettings.accessDenied')
        : settings.value
          ? t('configurationSettings.refreshFailed')
          : t('configurationSettings.loadFailed')
      return false
    } finally {
      if (current()) settingsLoading.value = false
    }
  }

  const saveSettings = async (
    newSettings: Pick<SystemSettings, 'registration_enabled'>,
  ): Promise<boolean> => {
    if (
      settingsLoading.value ||
      settingsSaving.value ||
      !settings.value ||
      typeof newSettings.registration_enabled !== 'boolean' ||
      newSettings.registration_enabled === settings.value.registration_enabled
    )
      return false
    const context = captureSession()
    const request = ++settingsRequest
    const current = () => request === settingsRequest && isSessionCurrent(context)
    settingsSaving.value = true
    settingsSaveError.value = null

    try {
      const response = await updateAdminSettings({
        settings: { registration_enabled: newSettings.registration_enabled },
      })
      if (!current()) return false
      acceptSettings(response.settings)
      settingsError.value = null
      return true
    } catch (error) {
      if (!current()) return false
      const denied = error instanceof ApiError && [401, 403].includes(error.status ?? 0)
      if (denied) clearSettings(true)
      settingsSaveError.value = t(
        denied ? 'configurationSettings.accessDenied' : 'configurationSettings.saveFailed',
      )
      return false
    } finally {
      if (current()) settingsSaving.value = false
    }
  }

  const saveTaskRetention = async (
    patch: ApiSchemas['TaskRetentionPatch'],
  ): Promise<'saved' | 'conflict' | 'failed' | 'stale'> => {
    if (
      settingsLoading.value ||
      settingsSaving.value ||
      !settings.value ||
      !validRetentionPolicy({ ...patch, revision: patch.expected_revision })
    )
      return 'failed'
    const baseline = settings.value.task_retention
    if (baseline.revision !== patch.expected_revision) return 'conflict'
    if (baseline.enabled === patch.enabled && baseline.retention_days === patch.retention_days)
      return 'saved'
    const context = captureSession(),
      request = ++settingsRequest
    const current = () => request === settingsRequest && isSessionCurrent(context)
    settingsSaving.value = true
    settingsSaveError.value = null
    try {
      const response = await updateAdminSettings({
        settings: {
          task_retention: {
            enabled: patch.enabled,
            retention_days: patch.retention_days,
            expected_revision: patch.expected_revision,
          },
        },
      })
      if (!current()) return 'stale'
      acceptSettings(response.settings)
      settingsError.value = null
      return 'saved'
    } catch (error) {
      if (!current()) return 'stale'
      if (error instanceof ApiError && [401, 403].includes(error.status ?? 0)) clearSettings(true)
      settingsSaveError.value = taskHistoryErrorMessage(error)
      return error instanceof ApiError &&
        error.status === 409 &&
        (!(error instanceof TaskHistoryApiError) || error.error_code === 'settings_conflict')
        ? 'conflict'
        : 'failed'
    } finally {
      if (current()) settingsSaving.value = false
    }
  }

  const loadAll = async (): Promise<void> => {
    await Promise.all([loadStats(), loadUsers()])
  }

  const resetUserFilters = (): void => {
    userSearchQuery.value = ''
    userRoleFilter.value = 'all'
    userActiveFilter.value = 'all'
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
    resetUserFilters,
    filteredUsers,
    auditLogs,
    auditLogsTotal,
    auditLogsLoading,
    auditLogsError,
    settings,
    settingsLoading,
    settingsError,
    settingsSaving,
    settingsSaveError,
    settingsAccessDenied,
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
    saveTaskRetention,
    acceptRetentionPolicy,
    clearSettings,
    loadAll,
  }
})
