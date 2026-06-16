import { computed, h, reactive, ref, type Ref } from 'vue'
import type { DataTableColumns, FormInst, FormRules } from 'naive-ui'
import { NButton, NSpace, NTag, NText, useMessage } from 'naive-ui'

import { type ApiSchemas } from '@/api/client'
import { useGlossaryStore } from '@/stores/glossary'
import { t } from '@/i18n'
import { formatDate } from '@/composables/useWorkspaceUtils'

type GlossaryEntry = ApiSchemas['GlossaryEntry']

export interface GlossaryFormModel {
  source: string
  target: string
  case_sensitive: boolean
  notes: string
}

export function useGlossaryManagement(projectId: Ref<number | null>) {
  const message = useMessage()
  const glossary = useGlossaryStore()

  // ── 状态 ──
  const glossaryDrawerVisible = ref(false)
  const editingGlossaryEntry = ref<GlossaryEntry | null>(null)
  const glossaryFormRef = ref<FormInst | null>(null)
  const glossaryImportVisible = ref(false)

  const glossaryForm = reactive<GlossaryFormModel>({
    source: '',
    target: '',
    case_sensitive: false,
    notes: '',
  })

  // ── 计算属性 ──
  const isGlossaryEditMode = computed(() => Boolean(editingGlossaryEntry.value))
  const glossaryDrawerTitle = computed(() =>
    isGlossaryEditMode.value
      ? t('workspace.segment.editTitle')
      : t('workspace.glossary.actions.create'),
  )

  const glossaryRules = computed<FormRules>(() => ({
    source: [
      {
        required: true,
        message: t('workspace.glossary.validation.sourceRequired'),
        trigger: ['input', 'blur'],
      },
    ],
    target: [
      {
        required: true,
        message: t('workspace.glossary.validation.targetRequired'),
        trigger: ['input', 'blur'],
      },
    ],
  }))

  // ── 表格列定义 ──
  const glossaryColumns = computed<DataTableColumns<GlossaryEntry>>(() => [
    {
      title: '#',
      key: 'id',
      width: 64,
      render: (_row, index) => `${index + 1}`,
    },
    {
      title: t('workspace.glossary.columns.source'),
      key: 'source',
      minWidth: 180,
      ellipsis: { tooltip: true },
    },
    {
      title: t('workspace.glossary.columns.target'),
      key: 'target',
      minWidth: 180,
      ellipsis: { tooltip: true },
    },
    {
      title: t('workspace.glossary.columns.caseSensitive'),
      key: 'case_sensitive',
      width: 120,
      render: (row) =>
        row.case_sensitive
          ? h(
              NTag,
              { size: 'small', type: 'info', bordered: false },
              { default: () => t('workspace.glossary.columns.caseSensitive') },
            )
          : h(NText, { depth: 3 }, { default: () => '—' }),
    },
    {
      title: t('workspace.glossary.columns.notes'),
      key: 'notes',
      minWidth: 160,
      ellipsis: { tooltip: true },
      render: (row) => row.notes || h(NText, { depth: 3 }, { default: () => '—' }),
    },
    {
      title: t('workspace.common.updatedAt'),
      key: 'updated_at',
      width: 170,
      render: (row) => formatDate(row.updated_at),
    },
    {
      title: t('workspace.common.actions'),
      key: 'actions',
      width: 160,
      fixed: 'right',
      render: (row) =>
        h(NSpace, { size: 4, wrap: false }, () => [
          h(
            NButton,
            {
              size: 'small',
              quaternary: true,
              type: 'primary',
              onClick: () => openEditGlossaryDrawer(row),
            },
            { default: () => t('workspace.segment.actions.edit') },
          ),
          h(
            NButton,
            {
              size: 'small',
              quaternary: true,
              type: 'error',
              loading: glossary.deletingEntryIds.includes(row.id),
              onClick: () => deleteGlossaryEntry(row),
            },
            { default: () => t('workspace.common.delete') },
          ),
        ]),
    },
  ])

  // ── 方法 ──
  const resetGlossaryForm = (): void => {
    glossaryForm.source = ''
    glossaryForm.target = ''
    glossaryForm.case_sensitive = false
    glossaryForm.notes = ''
  }

  const openCreateGlossaryDrawer = (): void => {
    editingGlossaryEntry.value = null
    resetGlossaryForm()
    glossaryDrawerVisible.value = true
  }

  const openEditGlossaryDrawer = (entry: GlossaryEntry): void => {
    editingGlossaryEntry.value = entry
    glossaryForm.source = entry.source
    glossaryForm.target = entry.target
    glossaryForm.case_sensitive = entry.case_sensitive
    glossaryForm.notes = entry.notes ?? ''
    glossaryDrawerVisible.value = true
  }

  const closeGlossaryDrawer = (): void => {
    glossaryDrawerVisible.value = false
    editingGlossaryEntry.value = null
    resetGlossaryForm()
  }

  const submitGlossaryEntry = async (): Promise<void> => {
    await glossaryFormRef.value?.validate()

    if (!projectId.value) {
      return
    }

    try {
      if (editingGlossaryEntry.value) {
        await glossary.updateEntry(projectId.value, editingGlossaryEntry.value.id, {
          source: glossaryForm.source.trim(),
          target: glossaryForm.target.trim(),
          case_sensitive: glossaryForm.case_sensitive,
          notes: glossaryForm.notes.trim() || undefined,
        })
        message.success(t('workspace.glossary.messages.updateSuccess'))
      } else {
        await glossary.createEntry(projectId.value, {
          source: glossaryForm.source.trim(),
          target: glossaryForm.target.trim(),
          case_sensitive: glossaryForm.case_sensitive,
          notes: glossaryForm.notes.trim() || undefined,
        })
        message.success(t('workspace.glossary.messages.createSuccess'))
      }

      closeGlossaryDrawer()
    } catch (error) {
      console.error(error)
      message.error(
        editingGlossaryEntry.value
          ? glossary.updateError || t('workspace.glossary.messages.updateFailed')
          : glossary.createError || t('workspace.glossary.messages.createFailed'),
      )
    }
  }

  const deleteGlossaryEntry = async (entry: GlossaryEntry): Promise<void> => {
    if (!projectId.value) {
      return
    }

    try {
      await glossary.deleteEntry(projectId.value, entry.id)
      message.success(t('workspace.glossary.messages.deleteSuccess'))
    } catch (error) {
      console.error(error)
      message.error(glossary.deleteError || t('workspace.glossary.messages.deleteFailed'))
    }
  }

  const handleGlossaryImport = async (file: File): Promise<void> => {
    if (!projectId.value) {
      return
    }

    try {
      const result = await glossary.importCSV(projectId.value, file)
      message.success(
        t('workspace.glossary.import.result', { added: result.added }) +
          (result.skipped?.length
            ? `，${t('workspace.glossary.import.skipped', { count: result.skipped.length })}`
            : ''),
      )
      glossaryImportVisible.value = false
    } catch (error) {
      console.error(error)
      message.error(glossary.importError || t('workspace.glossary.messages.importFailed'))
    }
  }

  const handleGlossaryExport = async (): Promise<void> => {
    if (!projectId.value) {
      return
    }

    try {
      await glossary.exportCSV(projectId.value)
    } catch (error) {
      console.error(error)
      message.error(t('workspace.glossary.messages.exportFailed'))
    }
  }

  return {
    // 状态
    glossaryDrawerVisible,
    editingGlossaryEntry,
    glossaryFormRef,
    glossaryImportVisible,
    glossaryForm,
    isGlossaryEditMode,
    glossaryDrawerTitle,
    glossaryRules,
    glossaryColumns,
    // 方法
    openCreateGlossaryDrawer,
    openEditGlossaryDrawer,
    closeGlossaryDrawer,
    submitGlossaryEntry,
    deleteGlossaryEntry,
    handleGlossaryImport,
    handleGlossaryExport,
  }
}
