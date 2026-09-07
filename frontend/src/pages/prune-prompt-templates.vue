<script setup lang="ts">
import {
  NButton,
  NDrawer,
  NDrawerContent,
  NForm,
  NFormItem,
  NInput,
  NModal,
  NSelect,
  NTag,
  useMessage,
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
type UpdateRequest = ApiSchemas['UpdatePrunePromptTemplateRequest']
type Scope = PrunePromptTemplate['scope']

interface FormModel {
  name: string
  description: string
  content: string
}

const store = usePrunePromptTemplatesStore()
const message = useMessage()
const { t } = useI18n()

const formRef = ref<FormInst | null>(null)
const drawerVisible = ref(false)
const editingItem = ref<PrunePromptTemplate | null>(null)
const deleteModalVisible = ref(false)
const deletingItem = ref<PrunePromptTemplate | null>(null)

const formModel = reactive<FormModel>({
  name: '',
  description: '',
  content: '',
})

const filterScopeOptions = computed<SelectOption[]>(() => [
  { label: t('prunePromptTemplates.filters.allScopes'), value: 'all' },
  { label: t('prunePromptTemplates.scopes.system'), value: 'system' },
  { label: t('prunePromptTemplates.scopes.user'), value: 'user' },
  { label: t('prunePromptTemplates.scopes.org'), value: 'org' },
])

const hasActiveFilters = computed(
  () => store.searchQuery.trim().length > 0 || store.scopeFilter !== 'all',
)

const metrics = computed(() => [
  { label: t('prunePromptTemplates.stats.total'), value: store.totalCount },
  { label: t('prunePromptTemplates.stats.system'), value: store.systemCount },
  { label: t('prunePromptTemplates.stats.user'), value: store.userCount },
  { label: t('prunePromptTemplates.stats.org'), value: store.orgCount },
])

const isEditMode = computed(() => Boolean(editingItem.value))
const isSystemScope = computed(() => editingItem.value?.scope === 'system')
const drawerTitle = computed(() =>
  isEditMode.value
    ? t('prunePromptTemplates.actions.edit')
    : t('prunePromptTemplates.actions.create'),
)

const rules = computed<FormRules>(() => ({
  name: [
    {
      required: true,
      message: t('prunePromptTemplates.validation.nameRequired'),
      trigger: ['input', 'blur'],
    },
  ],
}))

const resetForm = (): void => {
  formModel.name = ''
  formModel.description = ''
  formModel.content = ''
  editingItem.value = null
}

const openCreateDrawer = (): void => {
  resetForm()
  drawerVisible.value = true
}

const openEditDrawer = (item: PrunePromptTemplate): void => {
  editingItem.value = item
  formModel.name = item.name
  formModel.description = item.description ?? ''
  formModel.content = item.content ?? ''
  drawerVisible.value = true
}

const buildPayload = (): CreateRequest => {
  const payload: CreateRequest = { name: formModel.name.trim() }
  if (formModel.description.trim()) {
    payload.description = formModel.description.trim()
  }
  if (formModel.content.trim()) {
    payload.content = formModel.content.trim()
  }
  return payload
}

const onSubmit = async (): Promise<void> => {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }

  const payload = buildPayload()

  try {
    if (isEditMode.value && editingItem.value) {
      await store.updateTemplate(editingItem.value.id, payload as UpdateRequest)
      message.success(t('prunePromptTemplates.messages.updateSuccess'))
    } else {
      await store.createTemplate(payload)
      message.success(t('prunePromptTemplates.messages.createSuccess'))
    }
    drawerVisible.value = false
    resetForm()
  } catch {
    // Error is handled by the store
  }
}

const confirmDelete = (item: PrunePromptTemplate, event?: MouseEvent): void => {
  event?.stopPropagation()
  if (item.scope === 'system') {
    message.warning(t('prunePromptTemplates.messages.systemDeleteForbidden'))
    return
  }
  deletingItem.value = item
  deleteModalVisible.value = true
}

const executeDelete = async (): Promise<void> => {
  if (!deletingItem.value) return

  try {
    await store.deleteTemplate(deletingItem.value.id)
    message.success(t('prunePromptTemplates.messages.deleteSuccess'))
    deleteModalVisible.value = false
    deletingItem.value = null
  } catch {
    // Error is handled by the store
  }
}

const getScopeTagType = (scope: Scope): 'default' | 'info' | 'success' => {
  switch (scope) {
    case 'system':
      return 'default'
    case 'user':
      return 'info'
    case 'org':
      return 'success'
    default:
      return 'default'
  }
}

const formatDate = (dateStr: string | undefined): string => {
  if (!dateStr) return '—'
  return new Date(dateStr).toLocaleDateString()
}

const resetFilters = (): void => {
  store.searchQuery = ''
  store.scopeFilter = 'all'
}

onMounted(() => {
  store.loadTemplates()
})

watch(
  () => store.error,
  (err) => {
    if (err) {
      message.error(err, { duration: 0, closable: true })
      store.error = null
    }
  },
)
</script>

<template>
  <EntityListPage
    :title="t('prunePromptTemplates.title')"
    :subtitle="t('prunePromptTemplates.subtitle')"
    :metrics="metrics"
    :loading="store.loading"
    :empty="store.filteredItems.length === 0"
    :empty-description="
      hasActiveFilters
        ? t('prunePromptTemplates.empty.filtered')
        : t('prunePromptTemplates.empty.default')
    "
  >
    <template #actions>
      <NButton secondary :loading="store.loading" @click="store.loadTemplates">
        <template #icon><IconCarbonRenew /></template>
        {{ t('prunePromptTemplates.actions.refresh') }}
      </NButton>
      <NButton type="primary" @click="openCreateDrawer">
        <template #icon><IconCarbonAdd /></template>
        {{ t('prunePromptTemplates.actions.create') }}
      </NButton>
    </template>

    <template #filters>
      <NInput
        v-model:value="store.searchQuery"
        clearable
        class="lg:max-w-sm"
        :placeholder="t('prunePromptTemplates.filters.searchPlaceholder')"
      />
      <div class="flex flex-wrap gap-3">
        <NSelect v-model:value="store.scopeFilter" class="w-44" :options="filterScopeOptions" />
        <NButton v-if="hasActiveFilters" quaternary @click="resetFilters">
          {{ t('prunePromptTemplates.filters.reset') }}
        </NButton>
      </div>
    </template>

    <template #empty-extra>
      <NButton v-if="hasActiveFilters" secondary @click="resetFilters">
        {{ t('prunePromptTemplates.filters.reset') }}
      </NButton>
      <NButton v-else type="primary" @click="openCreateDrawer">
        {{ t('prunePromptTemplates.actions.createFirst') }}
      </NButton>
    </template>

    <!-- 卡片网格 -->
    <div class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <div
        v-for="item in store.filteredItems"
        :key="item.id"
        class="lf-interactive-card group flex h-full flex-col gap-4 p-5"
        @click="openEditDrawer(item)"
      >
        <div class="flex items-start justify-between gap-4">
          <div class="min-w-0">
            <h2 class="truncate text-lg font-semibold text-lf-text-strong">
              {{ item.name }}
            </h2>
          </div>
          <NTag round size="small" :type="getScopeTagType(item.scope)">
            {{ t(`prunePromptTemplates.scopes.${item.scope}`) }}
          </NTag>
        </div>

        <p
          class="line-clamp-2 text-sm leading-6 text-lf-text-muted"
          :class="{ 'italic text-lf-text-subtle': !item.description }"
        >
          {{ item.description || t('prunePromptTemplates.card.noDescription') }}
        </p>

        <div v-if="item.content" class="lf-code-panel line-clamp-3">
          {{ item.content }}
        </div>
        <p v-else class="text-xs italic text-lf-text-subtle">
          {{ t('prunePromptTemplates.card.noContent') }}
        </p>

        <div class="mt-auto border-t border-lf-border-soft pt-4">
          <div class="flex items-center justify-between gap-3">
            <span class="text-xs text-lf-text-subtle">
              {{ t('prunePromptTemplates.card.updatedAt') }} {{ formatDate(item.updated_at) }}
            </span>
            <div class="flex items-center gap-2" @click.stop>
              <NButton
                v-if="item.scope !== 'system'"
                text
                type="primary"
                class="font-medium"
                @click="openEditDrawer(item)"
              >
                {{ t('prunePromptTemplates.actions.edit') }}
              </NButton>
              <NButton
                v-if="item.scope !== 'system'"
                text
                type="error"
                class="font-medium"
                @click="confirmDelete(item, $event)"
              >
                {{ t('prunePromptTemplates.actions.delete') }}
              </NButton>
              <NButton
                v-if="item.scope === 'system'"
                text
                type="info"
                class="font-medium"
                @click="openEditDrawer(item)"
              >
                {{ t('prunePromptTemplates.actions.view') }}
              </NButton>
            </div>
          </div>
        </div>
      </div>
    </div>
  </EntityListPage>

  <NDrawer v-model:show="drawerVisible" :width="'min(640px, 100vw)'" placement="right">
    <NDrawerContent :native-scrollbar="false">
      <template #header>
        <div>
          <div class="text-lg font-semibold">{{ drawerTitle }}</div>
        </div>
      </template>

      <NForm
        ref="formRef"
        :model="formModel"
        :rules="rules"
        label-placement="top"
        require-mark-placement="right-hanging"
      >
        <NFormItem :label="t('prunePromptTemplates.form.name')" path="name">
          <NInput
            v-model:value="formModel.name"
            :placeholder="t('prunePromptTemplates.form.namePlaceholder')"
            :disabled="isSystemScope"
          />
        </NFormItem>

        <NFormItem :label="t('prunePromptTemplates.form.description')" path="description">
          <NInput
            v-model:value="formModel.description"
            type="textarea"
            :placeholder="t('prunePromptTemplates.form.descriptionPlaceholder')"
            :rows="3"
            :disabled="isSystemScope"
          />
        </NFormItem>

        <NFormItem :label="t('prunePromptTemplates.form.content')" path="content">
          <PromptTemplateEditor
            v-model="formModel.content"
            :disabled="isSystemScope"
            :rows="14"
            variable-set="prune"
          />
        </NFormItem>
      </NForm>

      <template #footer>
        <div class="flex justify-end gap-3">
          <NButton @click="drawerVisible = false">
            {{ t('prunePromptTemplates.actions.cancel') }}
          </NButton>
          <NButton
            v-if="!isSystemScope"
            type="primary"
            :loading="store.creating || store.updating"
            @click="onSubmit"
          >
            {{
              isEditMode
                ? t('prunePromptTemplates.actions.submitUpdate')
                : t('prunePromptTemplates.actions.submitCreate')
            }}
          </NButton>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>

  <NModal
    v-model:show="deleteModalVisible"
    preset="dialog"
    type="warning"
    :title="t('prunePromptTemplates.actions.confirmDelete')"
    :content="
      deletingItem ? t('prunePromptTemplates.delete.confirm', { name: deletingItem.name }) : ''
    "
    :positive-text="t('prunePromptTemplates.actions.confirmDelete')"
    :negative-text="t('prunePromptTemplates.actions.cancel')"
    :loading="deletingItem ? store.deletingIds.includes(deletingItem.id) : false"
    @positive-click="executeDelete"
  />
</template>
