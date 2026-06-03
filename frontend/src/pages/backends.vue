<script setup lang="ts">
import {
  NAlert,
  NButton,
  NDropdown,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NModal,
  NSelect,
  NSkeleton,
  NSlider,
  NSwitch,
  NTag,
  useMessage,
  type DropdownOption,
  type FormInst,
  type FormRules,
  type SelectOption,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import { useBackendsStore } from '@/stores/backends'

type Backend = ApiSchemas['Backend']
type BackendType = Backend['type']

interface BackendFormModel {
  name: string
  type: BackendType | null
  priority: number
  api_key: string
  base_url: string
  model: string
  temperature: number
  max_tokens: number
  timeout: number
  response_format: string
  enable_prompt_cache: boolean
}

const backends = useBackendsStore()
const message = useMessage()
const { t } = useI18n()
const formRef = ref<FormInst | null>(null)
const drawerVisible = ref(false)
const editingBackend = ref<Backend | null>(null)
const deleteModalVisible = ref(false)
const deletingBackend = ref<Backend | null>(null)

const formModel = reactive<BackendFormModel>({
  name: '',
  type: null,
  priority: 0,
  api_key: '',
  base_url: '',
  model: '',
  temperature: 0.2,
  max_tokens: 0,
  timeout: 60,
  response_format: 'json_schema',
  enable_prompt_cache: true,
})

const typeOptions = computed<SelectOption[]>(() => [
  { label: t('backends.types.openai'), value: 'openai' },
  { label: t('backends.types.anthropic'), value: 'anthropic' },
  { label: t('backends.types.google'), value: 'google' },
])

const filterTypeOptions = computed<SelectOption[]>(() => [
  { label: t('backends.filters.allTypes'), value: 'all' },
  ...typeOptions.value,
])

const responseFormatOptions = computed<SelectOption[]>(() => [
  { label: 'json_schema', value: 'json_schema' },
  { label: 'json_object', value: 'json_object' },
  { label: 'none', value: 'none' },
])

const hasActiveFilters = computed(
  () => backends.searchQuery.trim().length > 0 || backends.typeFilter !== 'all',
)

const isEditMode = computed(() => Boolean(editingBackend.value))
const drawerTitle = computed(() =>
  isEditMode.value ? t('backends.edit.title') : t('backends.create.title'),
)
const drawerDescription = computed(() =>
  isEditMode.value ? t('backends.edit.description') : t('backends.create.description'),
)
const submitting = computed(() => backends.creating || backends.updating)

const requiresApiKey = computed(() => Boolean(formModel.type))
const isAnthropic = computed(() => formModel.type === 'anthropic')

const rules = computed<FormRules>(() => ({
  name: [
    {
      required: true,
      message: t('backends.validation.nameRequired'),
      trigger: ['input', 'blur'],
    },
  ],
  type: [
    {
      required: true,
      message: t('backends.validation.typeRequired'),
      trigger: ['change', 'blur'],
    },
  ],
  api_key: [
    {
      required: requiresApiKey.value,
      message: t('backends.validation.apiKeyRequired'),
      trigger: ['input', 'blur'],
    },
  ],
}))

const resetForm = (): void => {
  formModel.name = ''
  formModel.type = null
  formModel.priority = 0
  formModel.api_key = ''
  formModel.base_url = ''
  formModel.model = ''
  formModel.temperature = 0.2
  formModel.max_tokens = 0
  formModel.timeout = 60
  formModel.response_format = 'json_schema'
  formModel.enable_prompt_cache = true
  editingBackend.value = null
}

const openCreateDrawer = (): void => {
  resetForm()
  drawerVisible.value = true
}

const openEditDrawer = (backend: Backend): void => {
  editingBackend.value = backend
  formModel.name = backend.name
  formModel.type = backend.type
  formModel.priority = backend.priority
  const opts = backend.options ?? {}
  formModel.api_key = typeof opts.api_key === 'string' ? opts.api_key : ''
  formModel.base_url = typeof opts.base_url === 'string' ? opts.base_url : ''
  formModel.model = typeof opts.model === 'string' ? opts.model : ''
  formModel.temperature = typeof opts.temperature === 'number' ? opts.temperature : 0.2
  formModel.max_tokens = typeof opts.max_tokens === 'number' ? opts.max_tokens : 0
  formModel.timeout = typeof opts.timeout === 'number' ? opts.timeout : 60
  formModel.response_format =
    typeof opts.response_format === 'string' ? opts.response_format : 'json_schema'
  formModel.enable_prompt_cache =
    typeof opts.enable_prompt_cache === 'boolean' ? opts.enable_prompt_cache : true
  drawerVisible.value = true
}

const buildOptions = (): Record<string, unknown> => {
  const options: Record<string, unknown> = {}

  if (formModel.api_key.trim()) {
    options.api_key = formModel.api_key.trim()
  }
  if (formModel.base_url.trim()) {
    options.base_url = formModel.base_url.trim()
  }
  if (formModel.model.trim()) {
    options.model = formModel.model.trim()
  }
  if (formModel.temperature !== 0.2) {
    options.temperature = formModel.temperature
  }
  if (formModel.max_tokens > 0) {
    options.max_tokens = formModel.max_tokens
  }
  if (formModel.timeout !== 60) {
    options.timeout = formModel.timeout
  }
  if (formModel.response_format !== 'json_schema') {
    options.response_format = formModel.response_format
  }
  if (isAnthropic.value && !formModel.enable_prompt_cache) {
    options.enable_prompt_cache = false
  }

  return options
}

const onSubmit = async (): Promise<void> => {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }

  if (!formModel.type) {
    return
  }

  const payload = {
    name: formModel.name.trim(),
    type: formModel.type,
    priority: formModel.priority,
    options: buildOptions(),
  }

  try {
    if (isEditMode.value && editingBackend.value) {
      await backends.updateBackend(editingBackend.value.id, payload)
      message.success(t('backends.messages.updateSuccess'))
    } else {
      await backends.createBackend(payload)
      message.success(t('backends.messages.createSuccess'))
    }
    drawerVisible.value = false
    resetForm()
  } catch {
    // Error is handled by the store
  }
}

const confirmDelete = (backend: Backend): void => {
  deletingBackend.value = backend
  deleteModalVisible.value = true
}

const executeDelete = async (): Promise<void> => {
  if (!deletingBackend.value) {
    return
  }

  try {
    await backends.deleteBackend(deletingBackend.value.id)
    message.success(t('backends.messages.deleteSuccess'))
    deleteModalVisible.value = false
    deletingBackend.value = null
  } catch {
    // Error is handled by the store
  }
}

const getBackendTypeTagType = (type: string): 'success' | 'warning' | 'info' | 'default' => {
  switch (type) {
    case 'openai': {
      return 'success'
    }
    case 'anthropic': {
      return 'warning'
    }
    case 'google': {
      return 'info'
    }
    default: {
      return 'default'
    }
  }
}

const getModelDisplay = (backend: Backend): string => {
  const opts = backend.options ?? {}
  if (typeof opts.model === 'string' && opts.model) {
    return opts.model
  }
  switch (backend.type) {
    case 'openai': {
      return 'gpt-4o-mini'
    }
    case 'anthropic': {
      return 'claude-sonnet-4-5'
    }
    case 'google': {
      return 'gemini-2.5-flash'
    }
    default: {
      return '-'
    }
  }
}

// eslint-disable-next-line @typescript-eslint/no-unused-vars
const buildCardActions = (backend: Backend): DropdownOption[] => [
  { label: t('projects.actions.edit'), key: 'edit' },
  { type: 'divider', key: 'divider' },
  { label: t('projects.actions.delete'), key: 'delete' },
]

const handleCardAction = (backend: Backend, key: string | number): void => {
  if (key === 'edit') {
    openEditDrawer(backend)
  } else if (key === 'delete') {
    confirmDelete(backend)
  }
}

onMounted(() => {
  backends.loadBackends()
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
            {{ t('backends.eyebrow') }}
          </div>
          <div>
            <h1 class="text-3xl font-semibold tracking-tight text-lf-text-strong">
              {{ t('backends.title') }}
            </h1>
            <p class="mt-2 max-w-2xl text-sm leading-6 text-lf-text-muted">
              {{ t('backends.subtitle') }}
            </p>
          </div>
        </div>
        <div class="flex flex-wrap gap-3">
          <NButton secondary :loading="backends.loading" @click="backends.loadBackends">
            {{ t('projects.actions.refresh') }}
          </NButton>
          <NButton type="primary" @click="openCreateDrawer">
            {{ t('backends.create.title') }}
          </NButton>
        </div>
      </div>
    </NCard>

    <!-- 统计卡片 -->
    <div class="grid grid-cols-1 gap-4 md:grid-cols-4">
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('backends.stats.total') }}
        </div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ backends.backendCount }}
        </div>
      </NCard>
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('backends.stats.openai') }}
        </div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ backends.openaiCount }}
        </div>
      </NCard>
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('backends.stats.anthropic') }}
        </div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ backends.anthropicCount }}
        </div>
      </NCard>
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('backends.stats.google') }}
        </div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ backends.googleCount }}
        </div>
      </NCard>
    </div>

    <!-- 筛选栏 -->
    <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
      <div class="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <NInput
          v-model:value="backends.searchQuery"
          clearable
          class="lg:max-w-sm"
          :placeholder="t('backends.filters.searchPlaceholder')"
        />
        <div class="flex flex-wrap gap-3">
          <NSelect v-model:value="backends.typeFilter" class="w-44" :options="filterTypeOptions" />
          <NButton
            v-if="hasActiveFilters"
            quaternary
            @click="((backends.searchQuery = ''), (backends.typeFilter = 'all'))"
          >
            {{ t('backends.filters.reset') }}
          </NButton>
        </div>
      </div>
    </NCard>

    <!-- 错误提示 -->
    <NAlert v-if="backends.error" type="error" :bordered="false">
      {{ backends.error }}
    </NAlert>

    <!-- 加载骨架屏 -->
    <div v-if="backends.loading" class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <NCard v-for="index in 6" :key="index" :bordered="false" class="shadow-sm shadow-lf-shadow">
        <NSkeleton text :repeat="4" />
      </NCard>
    </div>

    <!-- 空状态 -->
    <NEmpty
      v-else-if="backends.filteredItems.length === 0"
      class="rounded-2xl bg-lf-surface py-16 shadow-sm shadow-lf-shadow"
      :description="hasActiveFilters ? t('backends.empty.filtered') : t('backends.empty.default')"
    >
      <template #extra>
        <NButton
          v-if="hasActiveFilters"
          secondary
          @click="((backends.searchQuery = ''), (backends.typeFilter = 'all'))"
        >
          {{ t('backends.filters.reset') }}
        </NButton>
        <NButton v-else type="primary" @click="openCreateDrawer">
          {{ t('backends.create.title') }}
        </NButton>
      </template>
    </NEmpty>

    <!-- 后端卡片网格 -->
    <div v-else class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <NCard
        v-for="backend in backends.filteredItems"
        :key="backend.id"
        hoverable
        :bordered="false"
        class="group shadow-sm shadow-lf-shadow transition-all duration-200 hover:-translate-y-0.5 hover:shadow-lg hover:shadow-lf-shadow-strong"
      >
        <div class="flex h-full flex-col gap-5">
          <!-- 头部：类型标签 + 名称 + 操作 -->
          <div class="flex items-start justify-between gap-4">
            <div class="min-w-0">
              <h2 class="truncate text-lg font-semibold text-lf-text-strong">
                {{ backend.name }}
              </h2>
              <p class="mt-1 text-xs text-lf-text-subtle">ID #{{ backend.id }}</p>
            </div>
            <NTag round size="small" :type="getBackendTypeTagType(backend.type)">
              {{ t(`backends.types.${backend.type}`) }}
            </NTag>
          </div>

          <!-- 信息区 -->
          <div class="rounded-2xl bg-lf-surface-muted p-4">
            <div class="flex items-center justify-between gap-3">
              <div class="min-w-0">
                <div class="text-xs text-lf-text-subtle">
                  {{ t('backends.card.model') }}
                </div>
                <div class="mt-1 truncate font-mono text-sm font-semibold text-lf-text">
                  {{ getModelDisplay(backend) }}
                </div>
              </div>
              <div
                class="rounded-full bg-lf-surface-elevated px-3 py-1 text-xs font-medium text-lf-text-muted shadow-sm shadow-lf-shadow"
              >
                {{ t('backends.card.priority') }} {{ backend.priority }}
              </div>
            </div>
          </div>

          <!-- 操作区 -->
          <div class="mt-auto border-t border-lf-border-soft pt-4">
            <div class="flex items-center justify-between gap-3">
              <NButton text type="primary" class="font-medium" @click="openEditDrawer(backend)">
                {{ t('projects.actions.edit') }}
              </NButton>
              <NDropdown
                trigger="click"
                :options="buildCardActions(backend)"
                @select="(key) => handleCardAction(backend, key)"
              >
                <NButton quaternary size="small">
                  {{ t('projects.actions.more') }}
                </NButton>
              </NDropdown>
            </div>
          </div>
        </div>
      </NCard>
    </div>

    <!-- 创建/编辑抽屉 -->
    <NDrawer v-model:show="drawerVisible" :width="480" placement="right">
      <NDrawerContent :title="drawerTitle" :native-scrollbar="false">
        <template #header>
          <div>
            <div class="text-lg font-semibold">{{ drawerTitle }}</div>
            <div class="mt-1 text-xs text-lf-text-muted">{{ drawerDescription }}</div>
          </div>
        </template>

        <NForm
          ref="formRef"
          :model="formModel"
          :rules="rules"
          label-placement="top"
          require-mark-placement="right-hanging"
        >
          <NFormItem :label="t('backends.form.name')" path="name">
            <NInput
              v-model:value="formModel.name"
              :placeholder="t('backends.form.namePlaceholder')"
            />
          </NFormItem>

          <NFormItem :label="t('backends.form.type')" path="type">
            <NSelect
              v-model:value="formModel.type"
              :options="typeOptions"
              :placeholder="t('backends.form.typePlaceholder')"
              :disabled="isEditMode"
            />
          </NFormItem>

          <NFormItem :label="t('backends.form.priority')" path="priority">
            <div class="w-full">
              <NInputNumber
                v-model:value="formModel.priority"
                :min="0"
                :max="9999"
                class="w-full"
              />
              <div class="mt-1 text-xs text-lf-text-muted">
                {{ t('backends.form.priorityHint') }}
              </div>
            </div>
          </NFormItem>

          <NDivider />

          <NFormItem v-if="requiresApiKey" :label="t('backends.form.apiKey')" path="api_key">
            <NInput
              v-model:value="formModel.api_key"
              type="password"
              show-password-on="click"
              :placeholder="t('backends.form.apiKeyPlaceholder')"
            />
          </NFormItem>

          <NFormItem :label="t('backends.form.baseUrl')" path="base_url">
            <NInput
              v-model:value="formModel.base_url"
              :placeholder="t('backends.form.baseUrlPlaceholder')"
            />
          </NFormItem>

          <NFormItem :label="t('backends.form.model')" path="model">
            <NInput
              v-model:value="formModel.model"
              :placeholder="t('backends.form.modelPlaceholder')"
            />
          </NFormItem>

          <NFormItem :label="t('backends.form.temperature')" path="temperature">
            <div class="flex w-full items-center gap-3">
              <NSlider
                v-model:value="formModel.temperature"
                :min="0"
                :max="2"
                :step="0.1"
                class="flex-1"
              />
              <span class="w-10 text-right font-mono text-sm text-lf-text">
                {{ formModel.temperature.toFixed(1) }}
              </span>
            </div>
          </NFormItem>

          <NFormItem :label="t('backends.form.maxTokens')" path="max_tokens">
            <NInputNumber
              v-model:value="formModel.max_tokens"
              :min="0"
              :max="1000000"
              :placeholder="t('backends.form.maxTokensPlaceholder')"
              class="w-full"
            />
          </NFormItem>

          <NFormItem :label="t('backends.form.timeout')" path="timeout">
            <NInputNumber v-model:value="formModel.timeout" :min="1" :max="600" class="w-full" />
          </NFormItem>

          <NFormItem :label="t('backends.form.responseFormat')" path="response_format">
            <NSelect v-model:value="formModel.response_format" :options="responseFormatOptions" />
          </NFormItem>

          <NFormItem
            v-if="isAnthropic"
            :label="t('backends.form.enablePromptCache')"
            path="enable_prompt_cache"
          >
            <NSwitch v-model:value="formModel.enable_prompt_cache" />
          </NFormItem>
        </NForm>

        <template #footer>
          <div class="flex justify-end gap-3">
            <NButton @click="drawerVisible = false">
              {{ t('workspace.common.cancel') }}
            </NButton>
            <NButton type="primary" :loading="submitting" @click="onSubmit">
              {{ t('workspace.common.save') }}
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
      :title="t('projects.actions.confirmDelete')"
      :content="deletingBackend ? t('backends.delete.confirm', { name: deletingBackend.name }) : ''"
      :positive-text="t('workspace.common.confirm')"
      :negative-text="t('workspace.common.cancel')"
      :loading="deletingBackend ? backends.deletingBackendIds.includes(deletingBackend.id) : false"
      @positive-click="executeDelete"
    />
  </div>
</template>
