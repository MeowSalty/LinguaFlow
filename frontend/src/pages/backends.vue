<script setup lang="ts">
import {
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
type BackendOptions = ApiSchemas['BackendOptions']

interface BackendFormModel {
  name: string
  type: BackendType | null
  api_key: string
  base_url: string
  model: string
  temperatureEnabled: boolean
  temperature: number
  top_pEnabled: boolean
  top_p: number
  maxTokensEnabled: boolean
  max_tokens: number
  timeout: number
  response_format: string
  enable_prompt_cache: boolean
  stream: boolean
  rate_limit_per_minute: number
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
  api_key: '',
  base_url: '',
  model: '',
  temperatureEnabled: false,
  temperature: 0.2,
  top_pEnabled: false,
  top_p: 1.0,
  maxTokensEnabled: false,
  max_tokens: 0,
  timeout: 60,
  response_format: 'json_schema',
  enable_prompt_cache: true,
  stream: false,
  rate_limit_per_minute: 0,
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
  { label: 'text', value: 'text' },
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

const temperatureMax = computed(() => (formModel.type === 'anthropic' ? 1 : 2))
const maxTokensMin = computed(() => (formModel.type === 'openai' ? 0 : 1))
const maxTokensDefault = computed(() => (formModel.type === 'openai' ? 0 : 8192))

watch(
  () => formModel.type,
  () => {
    if (formModel.temperature > temperatureMax.value) {
      formModel.temperature = temperatureMax.value
    }
    if (formModel.max_tokens < maxTokensMin.value) {
      formModel.max_tokens = maxTokensMin.value
    }
  },
)

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
  formModel.api_key = ''
  formModel.base_url = ''
  formModel.model = ''
  formModel.temperatureEnabled = false
  formModel.temperature = 0.2
  formModel.top_pEnabled = false
  formModel.top_p = 1.0
  formModel.maxTokensEnabled = false
  formModel.max_tokens = 0
  formModel.timeout = 60
  formModel.response_format = 'json_schema'
  formModel.enable_prompt_cache = true
  formModel.stream = false
  formModel.rate_limit_per_minute = 0
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
  const opts = backend.options as Record<string, unknown> | undefined
  formModel.api_key = typeof opts?.api_key === 'string' ? opts.api_key : ''
  formModel.base_url = typeof opts?.base_url === 'string' ? opts.base_url : ''
  formModel.model = typeof opts?.model === 'string' ? opts.model : ''
  formModel.temperatureEnabled = typeof opts?.temperature === 'number'
  formModel.temperature =
    typeof opts?.temperature === 'number' ? Math.min(opts.temperature, temperatureMax.value) : 0.2
  formModel.top_pEnabled = typeof opts?.top_p === 'number'
  formModel.top_p = typeof opts?.top_p === 'number' ? opts.top_p : 1.0
  formModel.maxTokensEnabled = typeof opts?.max_tokens === 'number'
  formModel.max_tokens =
    typeof opts?.max_tokens === 'number'
      ? Math.max(opts.max_tokens, maxTokensMin.value)
      : maxTokensDefault.value
  formModel.timeout = typeof opts?.timeout === 'number' ? opts.timeout : 60
  formModel.response_format =
    typeof opts?.response_format === 'string' ? opts.response_format : 'json_schema'
  formModel.enable_prompt_cache =
    typeof opts?.enable_prompt_cache === 'boolean' ? opts.enable_prompt_cache : true
  formModel.stream = typeof opts?.stream === 'boolean' ? opts.stream : false
  formModel.rate_limit_per_minute = backend.rate_limit_per_minute ?? 0
  drawerVisible.value = true
}

const buildOptions = (): BackendOptions => {
  const options: Record<string, unknown> = {}

  options.type = formModel.type
  if (formModel.api_key.trim()) {
    options.api_key = formModel.api_key.trim()
  }
  if (formModel.base_url.trim()) {
    options.base_url = formModel.base_url.trim()
  }
  if (formModel.model.trim()) {
    options.model = formModel.model.trim()
  }
  if (formModel.temperatureEnabled) {
    options.temperature = formModel.temperature
  }
  if (formModel.top_pEnabled) {
    options.top_p = formModel.top_p
  }
  if (formModel.maxTokensEnabled) {
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
  if (formModel.stream) {
    options.stream = true
  }

  return options as BackendOptions
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
    options: buildOptions(),
    rate_limit_per_minute: formModel.rate_limit_per_minute,
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
  const opts = backend.options as Record<string, unknown> | undefined
  if (typeof opts?.model === 'string' && opts.model) {
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

watch(
  () => backends.error,
  (err) => {
    if (err) {
      message.error(err, { duration: 0, closable: true })
      backends.error = null
    }
  },
)
</script>

<template>
  <div class="lf-page">
    <section class="lf-page-header">
      <div class="flex flex-col gap-6 lg:flex-row lg:items-center lg:justify-between">
        <div class="space-y-3">
          <div class="lf-eyebrow">
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
    </section>

    <div class="grid grid-cols-1 gap-4 md:grid-cols-4">
      <div class="lf-metric">
        <div class="lf-metric-label">{{ t('backends.stats.total') }}</div>
        <div class="lf-metric-value">{{ backends.backendCount }}</div>
      </div>
      <div class="lf-metric">
        <div class="lf-metric-label">{{ t('backends.stats.openai') }}</div>
        <div class="lf-metric-value">{{ backends.openaiCount }}</div>
      </div>
      <div class="lf-metric">
        <div class="lf-metric-label">{{ t('backends.stats.anthropic') }}</div>
        <div class="lf-metric-value">{{ backends.anthropicCount }}</div>
      </div>
      <div class="lf-metric">
        <div class="lf-metric-label">{{ t('backends.stats.google') }}</div>
        <div class="lf-metric-value">{{ backends.googleCount }}</div>
      </div>
    </div>

    <div class="lf-panel px-4 py-3">
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
    </div>

    <div v-if="backends.loading" class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <div v-for="index in 6" :key="index" class="lf-panel p-5">
        <NSkeleton text :repeat="4" />
      </div>
    </div>

    <NEmpty
      v-else-if="backends.filteredItems.length === 0"
      class="lf-panel py-16"
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

    <div v-else class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <div
        v-for="backend in backends.filteredItems"
        :key="backend.id"
        class="lf-interactive-card group relative overflow-hidden p-5"
      >
        <div
          class="absolute inset-x-0 top-0 h-0.5 bg-gradient-to-r from-brand-500/0 via-lf-info/70 to-lf-accent/0 opacity-0 transition-opacity group-hover:opacity-100"
        />
        <div class="flex h-full flex-col gap-5">
          <div class="flex items-start justify-between gap-4">
            <div class="min-w-0">
              <h2 class="truncate text-lg font-semibold tracking-tight text-lf-text-strong">
                {{ backend.name }}
              </h2>
              <p class="mt-1 font-mono text-xs text-lf-text-subtle">ID #{{ backend.id }}</p>
            </div>
            <NTag round size="small" :bordered="false" :type="getBackendTypeTagType(backend.type)">
              {{ t(`backends.types.${backend.type}`) }}
            </NTag>
          </div>

          <div class="rounded-xl border border-lf-border-soft bg-lf-code-bg px-3.5 py-3">
            <div class="text-[11px] font-medium tracking-wide text-lf-text-subtle uppercase">
              {{ t('backends.card.model') }}
            </div>
            <div class="mt-1.5 truncate font-mono text-sm font-semibold text-lf-text-strong">
              {{ getModelDisplay(backend) }}
            </div>
          </div>

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
      </div>
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
              <NSwitch v-model:value="formModel.temperatureEnabled" />
              <template v-if="formModel.temperatureEnabled">
                <NSlider
                  v-model:value="formModel.temperature"
                  :min="0"
                  :max="temperatureMax"
                  :step="0.1"
                  class="flex-1"
                />
                <span class="w-10 text-right font-mono text-sm text-lf-text">
                  {{ formModel.temperature.toFixed(1) }}
                </span>
              </template>
              <span v-else class="text-xs text-lf-text-muted">
                {{ t('backends.form.useApiDefault') }}
              </span>
            </div>
          </NFormItem>

          <NFormItem :label="t('backends.form.topP')" path="top_p">
            <div class="flex w-full items-center gap-3">
              <NSwitch v-model:value="formModel.top_pEnabled" />
              <template v-if="formModel.top_pEnabled">
                <NSlider
                  v-model:value="formModel.top_p"
                  :min="0"
                  :max="1"
                  :step="0.05"
                  class="flex-1"
                />
                <span class="w-10 text-right font-mono text-sm text-lf-text">
                  {{ formModel.top_p.toFixed(2) }}
                </span>
              </template>
              <span v-else class="text-xs text-lf-text-muted">
                {{ t('backends.form.useApiDefault') }}
              </span>
            </div>
          </NFormItem>

          <NFormItem :label="t('backends.form.maxTokens')" path="max_tokens">
            <div class="flex w-full items-center gap-3">
              <NSwitch v-model:value="formModel.maxTokensEnabled" />
              <template v-if="formModel.maxTokensEnabled">
                <NInputNumber
                  v-model:value="formModel.max_tokens"
                  :min="maxTokensMin"
                  :max="1000000"
                  :placeholder="t('backends.form.maxTokensPlaceholder')"
                  class="flex-1"
                />
              </template>
              <span v-else class="text-xs text-lf-text-muted">
                {{ t('backends.form.useApiDefault') }}
              </span>
            </div>
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

          <NFormItem :label="t('backends.form.stream')" path="stream">
            <NSwitch v-model:value="formModel.stream" />
            <template #feedback>
              <span class="text-xs text-lf-text-muted">
                {{ t('backends.form.streamHint') }}
              </span>
            </template>
          </NFormItem>

          <NFormItem :label="t('backends.form.rateLimitPerMinute')" path="rate_limit_per_minute">
            <NInputNumber
              v-model:value="formModel.rate_limit_per_minute"
              :min="0"
              :placeholder="t('backends.form.rateLimitPerMinutePlaceholder')"
              class="w-full"
            />
            <template #feedback>
              <span class="text-xs text-lf-text-muted">
                {{ t('backends.form.rateLimitPerMinuteHint') }}
              </span>
            </template>
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
