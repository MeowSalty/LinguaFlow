import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  type ApiSchemas,
  createGlossaryEntry as createGlossaryEntryRequest,
  deleteGlossaryEntry as deleteGlossaryEntryRequest,
  exportGlossaryCSV as exportGlossaryCSVRequest,
  fetchGlossaryEntries,
  importGlossaryCSV as importGlossaryCSVRequest,
  updateGlossaryEntry as updateGlossaryEntryRequest,
} from '@/api/client'
import { t } from '@/i18n'

type GlossaryEntry = ApiSchemas['GlossaryEntry']
type CreateGlossaryEntryPayload = ApiSchemas['CreateGlossaryEntryRequest']
type UpdateGlossaryEntryPayload = ApiSchemas['UpdateGlossaryEntryRequest']
type GlossaryImportResult = ApiSchemas['GlossaryImportResult']

export const useGlossaryStore = defineStore('glossary', () => {
  const items = ref<GlossaryEntry[]>([])

  const loading = ref(false)
  const creating = ref(false)
  const updating = ref(false)
  const deletingEntryIds = ref<number[]>([])

  const error = ref<string | null>(null)
  const createError = ref<string | null>(null)
  const updateError = ref<string | null>(null)
  const deleteError = ref<string | null>(null)

  const importing = ref(false)
  const importError = ref<string | null>(null)
  const importResult = ref<GlossaryImportResult | null>(null)

  const searchQuery = ref('')

  const filteredItems = computed(() => {
    const query = searchQuery.value.trim().toLowerCase()

    return items.value.filter((entry) => {
      if (query.length === 0) {
        return true
      }

      return (
        entry.source.toLowerCase().includes(query) ||
        entry.target.toLowerCase().includes(query) ||
        (entry.notes?.toLowerCase().includes(query) ?? false)
      )
    })
  })

  const entryCount = computed(() => items.value.length)

  const loadEntries = async (projectId: number): Promise<void> => {
    loading.value = true
    error.value = null

    try {
      const response = await fetchGlossaryEntries(projectId)
      items.value = response.items
    } catch (loadError) {
      error.value =
        loadError instanceof Error ? loadError.message : t('api.errors.fetchGlossaryFailed')
    } finally {
      loading.value = false
    }
  }

  const createEntry = async (
    projectId: number,
    payload: CreateGlossaryEntryPayload,
  ): Promise<GlossaryEntry> => {
    creating.value = true
    createError.value = null

    try {
      const entry = await createGlossaryEntryRequest(projectId, payload)
      items.value = [...items.value, entry]
      return entry
    } catch (submitError) {
      createError.value =
        submitError instanceof Error
          ? submitError.message
          : t('api.errors.createGlossaryEntryFailed')
      throw submitError
    } finally {
      creating.value = false
    }
  }

  const updateEntry = async (
    projectId: number,
    entryId: number,
    payload: UpdateGlossaryEntryPayload,
  ): Promise<GlossaryEntry> => {
    updating.value = true
    updateError.value = null

    try {
      const entry = await updateGlossaryEntryRequest(projectId, entryId, payload)
      items.value = items.value.map((item) => (item.id === entry.id ? entry : item))
      return entry
    } catch (submitError) {
      updateError.value =
        submitError instanceof Error
          ? submitError.message
          : t('api.errors.updateGlossaryEntryFailed')
      throw submitError
    } finally {
      updating.value = false
    }
  }

  const deleteEntry = async (projectId: number, entryId: number): Promise<void> => {
    deletingEntryIds.value = [...deletingEntryIds.value, entryId]
    deleteError.value = null

    try {
      await deleteGlossaryEntryRequest(projectId, entryId)
      items.value = items.value.filter((item) => item.id !== entryId)
    } catch (submitError) {
      deleteError.value =
        submitError instanceof Error
          ? submitError.message
          : t('api.errors.deleteGlossaryEntryFailed')
      throw submitError
    } finally {
      deletingEntryIds.value = deletingEntryIds.value.filter((id) => id !== entryId)
    }
  }

  const importCSV = async (projectId: number, file: File): Promise<GlossaryImportResult> => {
    importing.value = true
    importError.value = null
    importResult.value = null

    try {
      const result = await importGlossaryCSVRequest(projectId, file)
      importResult.value = result
      await loadEntries(projectId)
      return result
    } catch (submitError) {
      importError.value =
        submitError instanceof Error ? submitError.message : t('api.errors.importGlossaryFailed')
      throw submitError
    } finally {
      importing.value = false
    }
  }

  const exportCSV = async (projectId: number): Promise<void> => {
    try {
      const blob = await exportGlossaryCSVRequest(projectId)
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = 'glossary.csv'
      anchor.click()
      URL.revokeObjectURL(url)
    } catch (submitError) {
      const errorMessage =
        submitError instanceof Error ? submitError.message : t('api.errors.exportGlossaryFailed')
      throw new Error(errorMessage)
    }
  }

  const reset = (): void => {
    items.value = []
    loading.value = false
    creating.value = false
    updating.value = false
    deletingEntryIds.value = []
    error.value = null
    createError.value = null
    updateError.value = null
    deleteError.value = null
    importing.value = false
    importError.value = null
    importResult.value = null
    searchQuery.value = ''
  }

  return {
    items,
    loading,
    creating,
    updating,
    deletingEntryIds,
    error,
    createError,
    updateError,
    deleteError,
    importing,
    importError,
    importResult,
    searchQuery,
    filteredItems,
    entryCount,
    loadEntries,
    createEntry,
    updateEntry,
    deleteEntry,
    importCSV,
    exportCSV,
    reset,
  }
})
