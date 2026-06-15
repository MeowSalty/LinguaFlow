<script setup lang="ts">
import {
  NAlert,
  NButton,
  NCard,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NForm,
  NFormItem,
  NInput,
  NModal,
  NSelect,
  NSkeleton,
  NTag,
  useMessage,
  type FormInst,
  type FormRules,
  type SelectOption,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import PromptTemplateEditor from '@/components/templates/PromptTemplateEditor.vue'
import { usePromptTemplatesStore } from '@/stores/promptTemplates'

type PromptTemplate = ApiSchemas['PromptTemplate']
type CreateRequest = ApiSchemas['CreatePromptTemplateRequest']
type UpdateRequest = ApiSchemas['UpdatePromptTemplateRequest']
type Scope = PromptTemplate['scope']

interface FormModel {
  name: string
  description: string
  system_prompt_content: string
}

// ── Store & 依赖 ──────────────────────────────────────────────

const store = usePromptTemplatesStore()
const message = useMessage()
const { t } = useI18n()

// ── 表单状态 ──────────────────────────────────────────────────

const formRef = ref<FormInst | null>(null)
const drawerVisible = ref(false)
const editingItem = ref<PromptTemplate | null>(null)
const deleteModalVisible = ref(false)
const deletingItem = ref<PromptTemplate | null>(null)

const formModel = reactive<FormModel>({
  name: '',
  description: '',
  system_prompt_content: '',
})

// ── 计算属性 ──────────────────────────────────────────────────

const filterScopeOptions = computed<SelectOption[]>(() => [
  { label: t('promptTemplates.filters.allScopes'), value: 'all' },
  { label: t('promptTemplates.scopes.system'), value: 'system' },
  { label: t('promptTemplates.scopes.user'), value: 'user' },
  { label: t('promptTemplates.scopes.org'), value: 'org' },
])

const hasActiveFilters = computed(
  () => store.searchQuery.trim().length > 0 || store.scopeFilter !== 'all',
)

const isEditMode = computed(() => Boolean(editingItem.value))
const isSystemScope = computed(() => editingItem.value?.scope === 'system')
const drawerTitle = computed(() =>
  isEditMode.value ? t('promptTemplates.actions.edit') : t('promptTemplates.actions.create'),
)

const rules = computed<FormRules>(() => ({
  name: [
    {
      required: true,
      message: t('promptTemplates.validation.nameRequired'),
      trigger: ['input', 'blur'],
    },
  ],
}))

// ── 方法 ──────────────────────────────────────────────────────

const resetForm = (): void => {
  formModel.name = ''
  formModel.description = ''
  formModel.system_prompt_content = ''
  editingItem.value = null
}

const openCreateDrawer = (): void => {
  resetForm()
  drawerVisible.value = true
}

const openEditDrawer = (item: PromptTemplate): void => {
  editingItem.value = item
  formModel.name = item.name
  formModel.description = item.description ?? ''
  formModel.system_prompt_content = item.system_prompt_content ?? ''
  drawerVisible.value = true
}

const buildPayload = (): CreateRequest => {
  const payload: CreateRequest = { name: formModel.name.trim() }
  if (formModel.description.trim()) {
    payload.description = formModel.description.trim()
  }
  if (formModel.system_prompt_content.trim()) {
    payload.system_prompt_content = formModel.system_prompt_content.trim()
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
      message.success(t('promptTemplates.messages.updateSuccess'))
    } else {
      await store.createTemplate(payload)
      message.success(t('promptTemplates.messages.createSuccess'))
    }
    drawerVisible.value = false
    resetForm()
  } catch {
    // Error is handled by the store
  }
}

const confirmDelete = (item: PromptTemplate): void => {
  if (item.scope === 'system') {
    message.warning(t('promptTemplates.messages.systemDeleteForbidden'))
    return
  }
  deletingItem.value = item
  deleteModalVisible.value = true
}

const executeDelete = async (): Promise<void> => {
  if (!deletingItem.value) return

  try {
    await store.deleteTemplate(deletingItem.value.id)
    message.success(t('promptTemplates.messages.deleteSuccess'))
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
  if (!dateStr) return t('promptTemplates.card.noDescription')
  return new Date(dateStr).toLocaleDateString()
}

// ── 生命周期 ──────────────────────────────────────────────────

onMounted(() => {
  store.loadTemplates()
})
</script>

<template>
  <div class="space-y-6">
    <!-- 页面头部 -->
    <NCard :bordered="false" class="overflow-hidden shadow-sm shadow-lf-shadow">
      <div class="flex flex-col gap-6 lg:flex-row lg:items-center lg:justify-between">
        <div class="space-y-3">
          <div
            class="inline-flex items-center rounded-full bg-lf-brand-soft px-3 py-1 text-xs font-medium text-brand-600"
          >
            {{ t('promptTemplates.eyebrow') }}
          </div>
          <div>
            <h1 class="text-3xl font-semibold tracking-tight text-lf-text-strong">
              {{ t('promptTemplates.title') }}
            </h1>
            <p class="mt-2 max-w-2xl text-sm leading-6 text-lf-text-muted">
              {{ t('promptTemplates.subtitle') }}
            </p>
          </div>
        </div>
        <div class="flex flex-wrap gap-3">
          <NButton secondary :loading="store.loading" @click="store.loadTemplates">
            {{ t('promptTemplates.actions.refresh') }}
          </NButton>
          <NButton type="primary" @click="openCreateDrawer">
            {{ t('promptTemplates.actions.create') }}
          </NButton>
        </div>
      </div>
    </NCard>

    <!-- 统计卡片 -->
    <div class="grid grid-cols-1 gap-4 md:grid-cols-4">
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('promptTemplates.stats.total') }}
        </div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ store.totalCount }}
        </div>
      </NCard>
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('promptTemplates.stats.system') }}
        </div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ store.systemCount }}
        </div>
      </NCard>
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('promptTemplates.stats.user') }}
        </div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ store.userCount }}
        </div>
      </NCard>
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('promptTemplates.stats.org') }}
        </div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ store.orgCount }}
        </div>
      </NCard>
    </div>

    <!-- 筛选栏 -->
    <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
      <div class="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <NInput
          v-model:value="store.searchQuery"
          clearable
          class="lg:max-w-sm"
          :placeholder="t('promptTemplates.filters.searchPlaceholder')"
        />
        <div class="flex flex-wrap gap-3">
          <NSelect v-model:value="store.scopeFilter" class="w-44" :options="filterScopeOptions" />
          <NButton
            v-if="hasActiveFilters"
            quaternary
            @click="((store.searchQuery = ''), (store.scopeFilter = 'all'))"
          >
            {{ t('promptTemplates.filters.reset') }}
          </NButton>
        </div>
      </div>
    </NCard>

    <!-- 错误提示 -->
    <NAlert v-if="store.error" type="error" :bordered="false">
      {{ store.error }}
    </NAlert>

    <!-- 加载骨架屏 -->
    <div v-if="store.loading" class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <NCard v-for="index in 6" :key="index" :bordered="false" class="shadow-sm shadow-lf-shadow">
        <NSkeleton text :repeat="4" />
      </NCard>
    </div>

    <!-- 空状态 -->
    <NEmpty
      v-else-if="store.filteredItems.length === 0"
      class="rounded-2xl bg-lf-surface py-16 shadow-sm shadow-lf-shadow"
      :description="
        hasActiveFilters ? t('promptTemplates.empty.filtered') : t('promptTemplates.empty.default')
      "
    >
      <template #extra>
        <NButton
          v-if="hasActiveFilters"
          secondary
          @click="((store.searchQuery = ''), (store.scopeFilter = 'all'))"
        >
          {{ t('promptTemplates.filters.reset') }}
        </NButton>
        <NButton v-else type="primary" @click="openCreateDrawer">
          {{ t('promptTemplates.actions.createFirst') }}
        </NButton>
      </template>
    </NEmpty>

    <!-- 卡片网格 -->
    <div v-else class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <NCard
        v-for="item in store.filteredItems"
        :key="item.id"
        hoverable
        :bordered="false"
        class="group shadow-sm shadow-lf-shadow transition-all duration-200 hover:-translate-y-0.5 hover:shadow-lg hover:shadow-lf-shadow-strong"
      >
        <div class="flex h-full flex-col gap-4">
          <!-- 头部：名称 + 作用域标签 -->
          <div class="flex items-start justify-between gap-4">
            <div class="min-w-0">
              <h2 class="truncate text-lg font-semibold text-lf-text-strong">
                {{ item.name }}
              </h2>
            </div>
            <NTag round size="small" :type="getScopeTagType(item.scope)">
              {{ t(`promptTemplates.scopes.${item.scope}`) }}
            </NTag>
          </div>

          <!-- 描述 -->
          <p
            class="line-clamp-2 text-sm leading-6 text-lf-text-muted"
            :class="{ 'italic text-lf-text-subtle': !item.description }"
          >
            {{ item.description || t('promptTemplates.card.noDescription') }}
          </p>

          <!-- 专属摘要：提示词内容预览 -->
          <div
            v-if="item.system_prompt_content"
            class="rounded-lg bg-lf-code-bg px-3 py-2 font-mono text-xs leading-5 text-lf-text-muted line-clamp-3"
          >
            {{ item.system_prompt_content }}
          </div>
          <p v-else class="text-xs italic text-lf-text-subtle">
            {{ t('promptTemplates.card.noPromptContent') }}
          </p>

          <!-- 底部：时间 + 操作 -->
          <div class="mt-auto border-t border-lf-border-soft pt-4">
            <div class="flex items-center justify-between gap-3">
              <span class="text-xs text-lf-text-subtle">
                {{ t('promptTemplates.card.createdAt') }} {{ formatDate(item.created_at) }}
              </span>
              <div class="flex items-center gap-2">
                <NButton
                  v-if="item.scope !== 'system'"
                  text
                  type="primary"
                  class="font-medium"
                  @click="openEditDrawer(item)"
                >
                  {{ t('promptTemplates.actions.edit') }}
                </NButton>
                <NButton
                  v-if="item.scope !== 'system'"
                  text
                  type="error"
                  class="font-medium"
                  @click="confirmDelete(item)"
                >
                  {{ t('promptTemplates.actions.delete') }}
                </NButton>
                <NButton
                  v-if="item.scope === 'system'"
                  text
                  type="info"
                  class="font-medium"
                  @click="openEditDrawer(item)"
                >
                  {{ t('promptTemplates.actions.view') }}
                </NButton>
              </div>
            </div>
          </div>
        </div>
      </NCard>
    </div>

    <!-- 创建/编辑抽屉 -->
    <NDrawer v-model:show="drawerVisible" :width="640" placement="right">
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
          <NFormItem :label="t('promptTemplates.form.name')" path="name">
            <NInput
              v-model:value="formModel.name"
              :placeholder="t('promptTemplates.form.namePlaceholder')"
              :disabled="isSystemScope"
            />
          </NFormItem>

          <NFormItem :label="t('promptTemplates.form.description')" path="description">
            <NInput
              v-model:value="formModel.description"
              type="textarea"
              :placeholder="t('promptTemplates.form.descriptionPlaceholder')"
              :rows="3"
              :disabled="isSystemScope"
            />
          </NFormItem>

          <NFormItem
            :label="t('promptTemplates.form.systemPromptContent')"
            path="system_prompt_content"
          >
            <PromptTemplateEditor
              v-model="formModel.system_prompt_content"
              :disabled="isSystemScope"
              :rows="6"
            />
          </NFormItem>
        </NForm>

        <template #footer>
          <div class="flex justify-end gap-3">
            <NButton @click="drawerVisible = false">
              {{ t('promptTemplates.actions.cancel') }}
            </NButton>
            <NButton
              v-if="!isSystemScope"
              type="primary"
              :loading="store.creating || store.updating"
              @click="onSubmit"
            >
              {{
                isEditMode
                  ? t('promptTemplates.actions.submitUpdate')
                  : t('promptTemplates.actions.submitCreate')
              }}
            </NButton>
          </div>
        </template>
      </NDrawerContent>
    </NDrawer>

    <!-- 删除确认弹窗 -->
    <NModal
      v-model:show="deleteModalVisible"
      preset="dialog"
      type="warning"
      :title="t('promptTemplates.actions.confirmDelete')"
      :content="
        deletingItem ? t('promptTemplates.delete.confirm', { name: deletingItem.name }) : ''
      "
      :positive-text="t('promptTemplates.actions.confirmDelete')"
      :negative-text="t('promptTemplates.actions.cancel')"
      :loading="deletingItem ? store.deletingIds.includes(deletingItem.id) : false"
      @positive-click="executeDelete"
    />
  </div>
</template>
