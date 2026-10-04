import { defineStore } from 'pinia'
import { onScopeDispose, ref } from 'vue'

import { type ApiSchemas, fetchProject } from '@/api/client'
import { t } from '@/i18n'
import { extractErrorMessage } from '@/utils/errors'
import { captureSession, isSessionCurrent, onSessionChange } from '@/api/session-context'
import { isAccessDenied } from '@/api/utils'

type Project = ApiSchemas['Project']

export const useProjectStore = defineStore('project', () => {
  const project = ref<Project | null>(null)
  /** 内部缓存当前项目 ID，供目录变化时自动预加载段落进度 */
  const _currentProjectId = ref<number | null>(null)

  // ── 加载状态 ──
  const loadingProject = ref(false)

  // ── 错误状态 ──
  const projectError = ref<string | null>(null)
  let requestRevision = 0

  // ── Actions ──

  const loadProject = async (projectId: number): Promise<void> => {
    if (_currentProjectId.value !== projectId) project.value = null
    const revision = ++requestRevision
    const session = captureSession()
    const current = () =>
      revision === requestRevision &&
      _currentProjectId.value === projectId &&
      isSessionCurrent(session)
    loadingProject.value = true
    projectError.value = null
    _currentProjectId.value = projectId

    try {
      const result = await fetchProject(projectId)
      if (current()) project.value = result
    } catch (error) {
      if (!current()) return
      if (isAccessDenied(error)) project.value = null
      projectError.value = extractErrorMessage(error, t('api.errors.fetchProjectFailed'))
    } finally {
      if (current()) loadingProject.value = false
    }
  }

  const reset = (): void => {
    requestRevision++
    loadingProject.value = false
    project.value = null
    _currentProjectId.value = null
    projectError.value = null
  }

  onScopeDispose(onSessionChange(reset))
  onScopeDispose(reset)

  return {
    project,
    _currentProjectId,
    loadingProject,
    projectError,
    loadProject,
    reset,
  }
})
