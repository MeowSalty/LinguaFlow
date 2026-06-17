import { defineStore } from 'pinia'
import { ref } from 'vue'

import { type ApiSchemas, fetchProject } from '@/api/client'
import { t } from '@/i18n'

type Project = ApiSchemas['Project']

const getErrorMessage = (error: unknown, fallback: string): string =>
  error instanceof Error ? error.message : fallback

export const useProjectStore = defineStore('project', () => {
  const project = ref<Project | null>(null)
  /** 内部缓存当前项目 ID，供目录变化时自动预加载段落进度 */
  const _currentProjectId = ref<number | null>(null)

  // ── 加载状态 ──
  const loadingProject = ref(false)

  // ── 错误状态 ──
  const projectError = ref<string | null>(null)

  // ── Actions ──

  const loadProject = async (projectId: number): Promise<void> => {
    loadingProject.value = true
    projectError.value = null
    _currentProjectId.value = projectId

    try {
      project.value = await fetchProject(projectId)
    } catch (error) {
      projectError.value = getErrorMessage(error, t('api.errors.fetchProjectFailed'))
    } finally {
      loadingProject.value = false
    }
  }

  const reset = (): void => {
    project.value = null
    _currentProjectId.value = null
    projectError.value = null
  }

  return {
    project,
    _currentProjectId,
    loadingProject,
    projectError,
    loadProject,
    reset,
  }
})
