<script setup lang="ts">
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NForm,
  NFormItem,
  NInput,
  NModal,
  NSelect,
  NTag,
  NText,
  useMessage,
  type DataTableColumns,
  type FormInst,
  type FormRules,
  type SelectOption,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import PromptTemplateEditor from '@/components/templates/PromptTemplateEditor.vue'
import { usePrunePromptTemplatesStore } from '@/stores/prunePromptTemplates'

type PrunePromptTemplate = ApiSchemas['PrunePromptTemplate']
type CreateRequest = ApiSchemas['CreatePrunePromptTemplateRequest']
type Scope = PrunePromptTemplate['scope']

const store = usePrunePromptTemplatesStore()
const message = useMessage()
const { t } = useI18n()

const formRef = ref<FormInst | null>(null)
const drawerVisible = ref(false)
const editingItem = ref<PrunePromptTemplate | null>(null)
const deletingItem = ref<PrunePromptTemplate | null>(null)
const formModel = reactive({ name: '', description: '', content: '' })

const isSystemScope = computed(() => editingItem.value?.scope === 'system')
const isEditMode = computed(() => editingItem.value !== null)
const hasActiveFilters = computed(
  () => store.searchQuery.trim().length > 0 || store.scopeFilter !== 'all',
)

const scopeOptions = computed<SelectOption[]>(() => [
  { label: t('prunePromptTemplates.filters.allScopes'), value: 'all' },
  { label: t('prunePromptTemplates.scopes.system'), value: 'system' },
  { label: t('prunePromptTemplates.scopes.user'), value: 'user' },
  { label: t('prunePromptTemplates.scopes.org'), value: 'org' },
])

const rules = computed<FormRules>(() => ({
  name: [
    {
      required: true,
      message: t('prunePromptTemplates.validation.nameRequired'),
      trigger: ['input', 'blur'],
    },
  ],
}))

const scopeTagType = (scope: Scope): 'default' | 'info' | 'success' =>
  scope === 'user' ? 'info' : scope === 'org' ? 'success' : 'default'

const formatDate = (value?: string): string => (value ? new Date(value).toLocaleDateString() : '—')

const openCreate = (): void => {
  editingItem.value = null
  Object.assign(formModel, { name: '', description: '', content: '' })
  drawerVisible.value = true
}

const openEdit = (item: PrunePromptTemplate): void => {
  editingItem.value = item
  Object.assign(formModel, {
    name: item.name,
    description: item.description ?? '',
    content: item.content ?? '',
  })
  drawerVisible.value = true
}

const submit = async (): Promise<void> => {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }

  const payload: CreateRequest = { name: formModel.name.trim() }
  if (formModel.description.trim()) payload.description = formModel.description.trim()
  if (formModel.content.trim()) payload.content = formModel.content.trim()

  try {
    if (editingItem.value) {
      await store.updateTemplate(editingItem.value.id, payload)
      message.success(t('prunePromptTemplates.messages.updateSuccess'))
    } else {
      await store.createTemplate(payload)
      message.success(t('prunePromptTemplates.messages.createSuccess'))
    }
    drawerVisible.value = false
  } catch {
    // Store exposes the request error through its error state.
  }
}

const remove = async (): Promise<void> => {
  if (!deletingItem.value) return
  try {
    await store.deleteTemplate(deletingItem.value.id)
    message.success(t('prunePromptTemplates.messages.deleteSuccess'))
    deletingItem.value = null
  } catch {
    // Store exposes the request error through its error state.
  }
}

const resetFilters = (): void => {
  store.searchQuery = ''
  store.scopeFilter = 'all'
}

const columns = computed<DataTableColumns<PrunePromptTemplate>>(() => [
  {
    title: t('prunePromptTemplates.columns.name'),
    key: 'name',
    minWidth: 190,
    render: (row) => h(NText, { strong: true }, { default: () => row.name }),
  },
  {
    title: t('prunePromptTemplates.columns.scope'),
    key: 'scope',
    width: 100,
    render: (row) =>
      h(
        NTag,
        { size: 'small', bordered: false, type: scopeTagType(row.scope) },
        { default: () => t(`prunePromptTemplates.scopes.${row.scope}`) },
      ),
  },
  {
    title: t('prunePromptTemplates.columns.description'),
    key: 'description',
    minWidth: 260,
    ellipsis: { tooltip: true },
    render: (row) => row.description || t('prunePromptTemplates.card.noDescription'),
  },
  {
    title: t('prunePromptTemplates.columns.updatedAt'),
    key: 'updated_at',
    width: 130,
    render: (row) => formatDate(row.updated_at),
  },
  {
    title: t('prunePromptTemplates.columns.actions'),
    key: 'actions',
    width: 150,
    fixed: 'right',
    render: (row) =>
      h('div', { class: 'flex gap-1' }, [
        h(
          NButton,
          { size: 'small', quaternary: true, type: 'primary', onClick: () => openEdit(row) },
          {
            default: () =>
              t(
                row.scope === 'system'
                  ? 'prunePromptTemplates.actions.view'
                  : 'prunePromptTemplates.actions.edit',
              ),
          },
        ),
        row.scope !== 'system'
          ? h(
              NButton,
              {
                size: 'small',
                quaternary: true,
                type: 'error',
                loading: store.deletingIds.includes(row.id),
                onClick: () => (deletingItem.value = row),
              },
              { default: () => t('prunePromptTemplates.actions.delete') },
            )
          : null,
      ]),
  },
])

onMounted(() => void store.loadTemplates())

watch(
  () => store.error,
  (error) => {
    if (error) message.error(error, { duration: 0, closable: true })
  },
)
</script>

<template>
  <div class="space-y-5">
    <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
      <div class="flex flex-col gap-4 md:flex-row md:items-center md:justify-between">
        <div>
          <div class="text-xs font-semibold uppercase text-brand-600">
            {{ t('prunePromptTemplates.eyebrow') }}
          </div>
          <h1 class="mt-2 text-2xl font-semibold text-lf-text-strong">
            {{ t('prunePromptTemplates.title') }}
          </h1>
          <p class="mt-2 max-w-2xl text-sm leading-6 text-lf-text-muted">
            {{ t('prunePromptTemplates.subtitle') }}
          </p>
        </div>
        <div class="flex gap-2">
          <NButton secondary :loading="store.loading" @click="store.loadTemplates">
            <template #icon><IconCarbonRenew /></template>
            {{ t('prunePromptTemplates.actions.refresh') }}
          </NButton>
          <NButton type="primary" @click="openCreate">
            <template #icon><IconCarbonAdd /></template>
            {{ t('prunePromptTemplates.actions.create') }}
          </NButton>
        </div>
      </div>
    </NCard>

    <NAlert v-if="store.error" type="error" :bordered="false">{{ store.error }}</NAlert>

    <div class="flex flex-col gap-3 md:flex-row">
      <NInput
        v-model:value="store.searchQuery"
        clearable
        :placeholder="t('prunePromptTemplates.filters.searchPlaceholder')"
      />
      <NSelect v-model:value="store.scopeFilter" class="md:w-48" :options="scopeOptions" />
      <NButton v-if="hasActiveFilters" quaternary @click="resetFilters">
        {{ t('prunePromptTemplates.filters.reset') }}
      </NButton>
    </div>

    <NDataTable
      :columns="columns"
      :data="store.filteredItems"
      :loading="store.loading"
      :row-key="(row: PrunePromptTemplate) => row.id"
      :scroll-x="900"
    >
      <template #empty>
        <NEmpty
          class="py-14"
          :description="
            hasActiveFilters
              ? t('prunePromptTemplates.empty.filtered')
              : t('prunePromptTemplates.empty.default')
          "
        />
      </template>
    </NDataTable>

    <NDrawer v-model:show="drawerVisible" :width="640" placement="right">
      <NDrawerContent
        :title="
          t(
            isEditMode
              ? 'prunePromptTemplates.actions.edit'
              : 'prunePromptTemplates.actions.create',
          )
        "
        closable
        :native-scrollbar="false"
      >
        <NForm ref="formRef" :model="formModel" :rules="rules" label-placement="top">
          <NFormItem path="name" :label="t('prunePromptTemplates.form.name')">
            <NInput v-model:value="formModel.name" :disabled="isSystemScope" />
          </NFormItem>
          <NFormItem path="description" :label="t('prunePromptTemplates.form.description')">
            <NInput
              v-model:value="formModel.description"
              type="textarea"
              :rows="3"
              :disabled="isSystemScope"
            />
          </NFormItem>
          <NFormItem path="content" :label="t('prunePromptTemplates.form.content')">
            <PromptTemplateEditor
              v-model="formModel.content"
              :disabled="isSystemScope"
              :rows="14"
              variable-set="prune"
            />
          </NFormItem>
        </NForm>
        <template #footer>
          <div class="flex justify-end gap-2">
            <NButton @click="drawerVisible = false">
              {{ t('prunePromptTemplates.actions.cancel') }}
            </NButton>
            <NButton
              v-if="!isSystemScope"
              type="primary"
              :loading="store.creating || store.updating"
              @click="submit"
            >
              {{ t('prunePromptTemplates.actions.save') }}
            </NButton>
          </div>
        </template>
      </NDrawerContent>
    </NDrawer>

    <NModal
      :show="deletingItem !== null"
      preset="dialog"
      type="warning"
      :title="t('prunePromptTemplates.actions.confirmDelete')"
      :content="
        deletingItem ? t('prunePromptTemplates.delete.confirm', { name: deletingItem.name }) : ''
      "
      :positive-text="t('prunePromptTemplates.actions.delete')"
      :negative-text="t('prunePromptTemplates.actions.cancel')"
      @update:show="(show) => !show && (deletingItem = null)"
      @positive-click="remove"
    />
  </div>
</template>
