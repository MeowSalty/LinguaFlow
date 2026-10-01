<script setup lang="ts">
import {
  NAlert,
  NButton,
  NDivider,
  NDropdown,
  NDrawer,
  NDrawerContent,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NModal,
  NSelect,
  NSlider,
  NSwitch,
  NTab,
  NTag,
  NTabs,
  useDialog,
  useMessage,
  type DropdownOption,
  type FormInst,
  type FormRules,
  type SelectOption,
} from 'naive-ui'
import { computed, h, onScopeDispose, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { listBackendModels, type ApiSchemas } from '@/api/client'
import { useBackendsStore } from '@/stores/backends'
import { useCredentialsStore } from '@/stores/credentials'
import { useStoreErrorToast } from '@/composables/useStoreErrorToast'
import { DRAWER_WIDTH } from '@/components/common/uiConstants'
import { useOrganizationScope } from '@/composables/useOrganizationScope'
import OrganizationScopeSelect from '@/components/organizations/OrganizationScopeSelect.vue'
import CredentialManager from '@/components/backends/CredentialManager.vue'
import {
  canManageOrganization,
  onOrganizationInvalidated,
  organizationRoles,
} from '@/utils/organization-scope'
import { captureSession, isSessionCurrent, onSessionChange } from '@/api/session-context'
import {
  createBackendForm,
  buildBackendPayload,
  backendBindingUnchanged,
  type BackendFormModel,
} from '@/utils/backend-form'
import {
  isUnknownCredentialWrite,
  isCredentialAccessDenied,
  safeCredentialError,
} from '@/api/credential-errors'

type Backend = ApiSchemas['Backend']
type BackendType = Backend['type']
type ThinkingLevel = ApiSchemas['ThinkingLevel']
const THINKING_LEVELS: ThinkingLevel[] = ['off', 'minimal', 'low', 'medium', 'high']
const DEFAULT_THINKING_LEVEL: ThinkingLevel = 'low'
const backends = useBackendsStore(),
  credentials = useCredentialsStore()
const { orgId, canWrite, setScope } = useOrganizationScope((id) => {
  backends.setOrganization(id)
  void backends.loadBackends(id)
})
const { t } = useI18n()
const message = useMessage(),
  dialog = useDialog()
const formRef = ref<FormInst | null>(null)
const drawerVisible = ref(false),
  managerVisible = ref(false),
  deleteModalVisible = ref(false)
const editingBackend = ref<Backend | null>(null),
  deletingBackend = ref<Backend | null>(null)
const modelOptions = ref<SelectOption[]>([]),
  fetchingModels = ref(false)
const credentialError = ref<string | null>(null),
  unknownWrite = ref(false),
  saving = ref(false)
const pendingBackendWrites = ref(0)
let formGeneration = 0,
  modelFetchGeneration = 0
let hydrating = false,
  clearingProbe = false
let probeController = new AbortController()
const formModel = reactive<BackendFormModel>(createBackendForm())
const typeOptions = computed<SelectOption[]>(() =>
  ['openai', 'anthropic', 'google'].map((value) => ({
    label: t(`backends.types.${value}`),
    value,
  })),
)
const filterTabs = computed(() => [
  { name: 'all', label: t('backends.filters.all'), count: backends.backendCount },
  { name: 'openai', label: t('backends.types.openai'), count: backends.openaiCount },
  { name: 'anthropic', label: t('backends.types.anthropic'), count: backends.anthropicCount },
  { name: 'google', label: t('backends.types.google'), count: backends.googleCount },
])
const renderFilterTab = (tab: (typeof filterTabs.value)[number]) =>
  h('span', { class: 'inline-flex items-baseline gap-1.5' }, [
    tab.label,
    h(
      'span',
      { class: 'hidden text-xs font-normal text-lf-text-subtle sm:inline' },
      String(tab.count),
    ),
  ])
const responseFormatOptions = computed<SelectOption[]>(() => [
  { label: t('backends.form.responseFormatOptions.jsonSchema'), value: 'json_schema' },
  { label: t('backends.form.responseFormatOptions.jsonObject'), value: 'json_object' },
  { label: t('backends.form.responseFormatOptions.text'), value: 'text' },
  { label: t('backends.form.responseFormatOptions.none'), value: 'none' },
])
const formatThinkingTooltip = (value: number): string =>
  THINKING_LEVELS[value]
    ? t(`backends.form.thinkingLevels.${THINKING_LEVELS[value]}`)
    : String(value)
const hasActiveFilters = computed(
  () => !!backends.searchQuery.trim() || backends.typeFilter !== 'all',
)
const isEditMode = computed(() => !!editingBackend.value)
const drawerTitle = computed(() =>
  t(isEditMode.value ? 'backends.edit.title' : 'backends.create.title'),
)
const drawerDescription = computed(() =>
  t(isEditMode.value ? 'configurationCredentials.impact' : 'backends.create.description'),
)
const submitting = computed(() => saving.value || backends.creating || backends.updating)
const isReadOnly = computed(() => !backends.canEdit(editingBackend.value ?? undefined))
const isAnthropic = computed(() => formModel.type === 'anthropic')
const isThinkingEnabled = computed(
  () => formModel.thinkingEnabled && formModel.thinking_level !== 'off',
)
const thinkingLevelIndex = computed({
  get: () => THINKING_LEVELS.indexOf(formModel.thinking_level),
  set: (value: number) => {
    const level = THINKING_LEVELS[value]
    if (level) formModel.thinking_level = level
  },
})
const samplingControlsDisabled = computed(() => isAnthropic.value && isThinkingEnabled.value)
const probeSecret = computed(() =>
  formModel.credentialMode === 'new' ? formModel.secret : formModel.probeSecret,
)
const canFetchModels = computed(
  () => !!formModel.type && !!probeSecret.value.trim() && !submitting.value,
)
const hasModelOptions = computed(() => modelOptions.value.length > 0)
const temperatureMax = computed(() => (isAnthropic.value ? 1 : 2))
const maxTokensMin = computed(() => (formModel.type === 'openai' ? 0 : 1))
const maxTokensDefault = computed(() => (formModel.type === 'openai' ? 0 : 8192))
const parseThinkingLevel = (value: unknown): ThinkingLevel | undefined =>
  typeof value === 'string' && (THINKING_LEVELS as string[]).includes(value)
    ? (value as ThinkingLevel)
    : undefined
const credentialScope = computed(() => editingBackend.value?.owner_org_id ?? orgId.value)
const canManageCredentials = computed(
  () =>
    credentialScope.value === null ||
    canManageOrganization(organizationRoles.value[credentialScope.value]),
)
const bindingUnchanged = computed(() => backendBindingUnchanged(formModel, editingBackend.value))
const credentialModeOptions = computed(() => [
  ...(isEditMode.value
    ? [
        {
          label: t('configurationCredentials.keep'),
          value: 'keep',
          disabled: !bindingUnchanged.value,
        },
      ]
    : []),
  { label: t('configurationCredentials.new'), value: 'new' },
  ...(canManageCredentials.value
    ? [{ label: t('configurationCredentials.existing'), value: 'existing' }]
    : []),
])
const credentialOptions = computed(() =>
  credentials.items
    .filter((item) => item.provider === formModel.type)
    .map((item) => ({
      label: `#${item.id} ? ${item.endpoint} ? v${item.current_version}`,
      value: item.id,
    })),
)
const clearSecrets = () => {
  formModel.secret = formModel.probeSecret = ''
  formModel.credentialId = null
}
const invalidateModelProbe = () => {
  ++modelFetchGeneration
  probeController.abort()
  probeController = new AbortController()
  modelOptions.value = []
  fetchingModels.value = false
}
const formContext = () => {
  const session = captureSession(),
    generation = formGeneration,
    scope = orgId.value
  return () =>
    isSessionCurrent(session) &&
    generation === formGeneration &&
    scope === orgId.value &&
    drawerVisible.value
}
const refreshCredentialChoices = () =>
  canManageCredentials.value ? credentials.load(credentialScope.value) : Promise.resolve(false)
const refreshBackends = async (): Promise<boolean> => {
  const session = captureSession(),
    scope = orgId.value
  await backends.loadBackends(scope)
  return isSessionCurrent(session) && orgId.value === scope && !backends.error
}
const openManager = () => {
  drawerVisible.value = false
  managerVisible.value = true
}
watch(
  () => [formModel.type, formModel.base_url] as const,
  ([type], [oldType]) => {
    if (hydrating) return
    clearSecrets()
    credentialError.value = null
    invalidateModelProbe()
    if (type !== oldType) {
      formModel.temperature = Math.min(formModel.temperature, temperatureMax.value)
      formModel.max_tokens = Math.max(formModel.max_tokens, maxTokensMin.value)
    }
  },
  { flush: 'sync' },
)
watch(
  () => formModel.credentialMode,
  (mode, old) => {
    if (hydrating) return
    if (old === 'new') formModel.secret = ''
    if (old === 'existing') formModel.credentialId = null
    formModel.probeSecret = ''
    credentialError.value = null
    invalidateModelProbe()
    if (mode === 'existing' && drawerVisible.value) void refreshCredentialChoices()
  },
  { flush: 'sync' },
)
watch(
  () => [formModel.secret, formModel.probeSecret],
  () => {
    if (!clearingProbe) invalidateModelProbe()
  },
  { flush: 'sync' },
)
watch(
  drawerVisible,
  (visible) => {
    ++formGeneration
    if (!visible) {
      clearSecrets()
      invalidateModelProbe()
      saving.value = false
    }
  },
  { flush: 'sync' },
)
const rules = computed<FormRules>(() => ({
  name: [
    { required: true, message: t('backends.validation.nameRequired'), trigger: ['input', 'blur'] },
  ],
  type: [
    { required: true, message: t('backends.validation.typeRequired'), trigger: ['change', 'blur'] },
  ],
  model: [
    { required: true, message: t('backends.validation.modelRequired'), trigger: ['input', 'blur'] },
  ],
  credentialMode: [
    {
      validator: () => {
        if (formModel.credentialMode === 'keep')
          return bindingUnchanged.value || new Error(t('configurationCredentials.rebind'))
        if (formModel.credentialMode === 'new')
          return !!formModel.secret.trim() || new Error(t('configurationCredentials.required'))
        return (
          (canManageCredentials.value &&
            credentials.orgId === credentialScope.value &&
            credentials.ready &&
            credentialOptions.value.some((item) => item.value === formModel.credentialId)) ||
          new Error(t('configurationCredentials.unavailable'))
        )
      },
      trigger: ['change', 'blur'],
    },
  ],
}))
const resetForm = () => {
  ++formGeneration
  hydrating = true
  Object.assign(formModel, createBackendForm())
  editingBackend.value = null
  credentialError.value = null
  unknownWrite.value = false
  hydrating = false
  invalidateModelProbe()
}
const filterModelOption = (pattern: string, option: SelectOption) =>
  `${option.label ?? ''} ${option.value ?? ''}`.toLowerCase().includes(pattern.trim().toLowerCase())
async function handleFetchModels(): Promise<void> {
  if (!canFetchModels.value || !formModel.type || fetchingModels.value) return
  const type = formModel.type,
    secret = probeSecret.value.trim(),
    base = formModel.base_url.trim(),
    generation = ++modelFetchGeneration,
    current = formContext()
  fetchingModels.value = true
  try {
    const response = await listBackendModels(
      { type, secret, ...(base ? { base_url: base } : {}) },
      undefined,
      probeController.signal,
    )
    if (!current() || generation !== modelFetchGeneration) return
    modelOptions.value = response.items.map((item) => ({
      label: item.name && item.name !== item.id ? `${item.name} (${item.id})` : item.id,
      value: item.id,
    }))
    if (formModel.credentialMode !== 'new') {
      clearingProbe = true
      formModel.probeSecret = ''
      clearingProbe = false
    }
    if (!response.items.length) message.info(t('backends.form.fetchModelsEmpty'))
    else {
      message.success(t('backends.form.fetchModelsSuccess', { count: response.items.length }))
      if (!formModel.model.trim()) formModel.model = response.items[0]!.id
    }
  } catch (cause) {
    if (current() && generation === modelFetchGeneration)
      message.error(safeCredentialError(cause).message)
  } finally {
    if (current() && generation === modelFetchGeneration) fetchingModels.value = false
  }
}
function fillFormFromBackend(backend: Backend): void {
  const opts = backend.options as unknown as Record<string, unknown> | undefined
  formModel.name = backend.name
  formModel.type = backend.type
  clearSecrets()
  formModel.base_url = typeof opts?.base_url === 'string' ? opts.base_url : ''
  formModel.model = typeof opts?.model === 'string' ? opts.model : ''
  formModel.temperatureEnabled = typeof opts?.temperature === 'number'
  formModel.temperature =
    typeof opts?.temperature === 'number' ? Math.min(opts.temperature, temperatureMax.value) : 0.2
  formModel.top_pEnabled = typeof opts?.top_p === 'number'
  formModel.top_p = typeof opts?.top_p === 'number' ? opts.top_p : 1
  formModel.maxTokensEnabled = typeof opts?.max_tokens === 'number'
  formModel.max_tokens =
    typeof opts?.max_tokens === 'number'
      ? Math.max(opts.max_tokens, maxTokensMin.value)
      : maxTokensDefault.value
  const timeout = typeof opts?.timeout === 'number' ? opts.timeout : 60
  formModel.timeoutEnabled = timeout > 0
  formModel.timeout = timeout > 0 ? timeout : 60
  formModel.response_format = ['json_schema', 'json_object', 'text', 'none'].includes(
    String(opts?.response_format),
  )
    ? (opts!.response_format as ApiSchemas['ResponseFormat'])
    : 'json_schema'
  formModel.enable_prompt_cache =
    typeof opts?.enable_prompt_cache === 'boolean' ? opts.enable_prompt_cache : true
  formModel.stream = typeof opts?.stream === 'boolean' ? opts.stream : false
  const thinkingLevel = parseThinkingLevel(opts?.thinking_level)
  formModel.thinkingEnabled = thinkingLevel !== undefined
  formModel.thinking_level = thinkingLevel ?? DEFAULT_THINKING_LEVEL
  formModel.rate_limit_per_minute = backend.rate_limit_per_minute ?? 0
}
const openCreateDrawer = () => {
  resetForm()
  drawerVisible.value = true
}
const openEditDrawer = (backend: Backend) => {
  resetForm()
  hydrating = true
  editingBackend.value = backend
  fillFormFromBackend(backend)
  formModel.credentialMode = 'keep'
  hydrating = false
  drawerVisible.value = true
}
const openCopyDrawer = (backend: Backend) => {
  resetForm()
  hydrating = true
  fillFormFromBackend(backend)
  formModel.name = t('backends.copy.name', { name: backend.name })
  formModel.credentialMode = 'new'
  hydrating = false
  drawerVisible.value = true
}
async function onSubmit(retryUnknown = false): Promise<void> {
  if (isReadOnly.value || submitting.value) return
  if (unknownWrite.value && !retryUnknown) {
    const current = formContext()
    dialog.warning({
      title: t('common.confirm'),
      content: t('configurationCredentials.retryWriteConfirm'),
      positiveText: t('common.confirm'),
      negativeText: t('common.cancel'),
      onPositiveClick: () => {
        if (current()) void onSubmit(true)
      },
    })
    return
  }
  const current = formContext()
  const writeSession = captureSession(),
    writeScope = orgId.value
  pendingBackendWrites.value++
  saving.value = true
  try {
    try {
      await formRef.value?.validate()
    } catch {
      return
    }
    if (!current() || isReadOnly.value) return
    const scope = credentialScope.value,
      payload = buildBackendPayload(formModel, editingBackend.value)
    credentialError.value = null
    unknownWrite.value = false
    if (editingBackend.value) await backends.updateBackend(editingBackend.value.id, payload)
    else await backends.createBackend(payload)
    if (!current()) return
    clearSecrets()
    message.success(
      t(isEditMode.value ? 'backends.messages.updateSuccess' : 'backends.messages.createSuccess'),
    )
    drawerVisible.value = false
    resetForm()
    if (credentials.loaded && credentials.orgId === scope) void credentials.load(scope)
  } catch (cause) {
    if (isSessionCurrent(writeSession) && writeScope === orgId.value) backends.error = null
    if (!current()) return
    if (isCredentialAccessDenied(cause)) {
      credentials.invalidate()
      drawerVisible.value = false
      message.error(t('configurationCredentials.denied'))
      return
    }
    unknownWrite.value = isUnknownCredentialWrite(cause)
    credentialError.value = unknownWrite.value
      ? t('configurationCredentials.unknown')
      : safeCredentialError(cause).message
  } finally {
    pendingBackendWrites.value--
    if (current()) saving.value = false
  }
}
const confirmDelete = (backend: Backend) => {
  deletingBackend.value = backend
  deleteModalVisible.value = true
}
async function executeDelete(): Promise<void> {
  const backend = deletingBackend.value
  if (!backend || backends.deletingBackendIds.includes(backend.id)) return
  const session = captureSession(),
    generation = formGeneration,
    scope = orgId.value
  try {
    await backends.deleteBackend(backend.id)
    if (
      !isSessionCurrent(session) ||
      generation !== formGeneration ||
      scope !== orgId.value ||
      deletingBackend.value?.id !== backend.id
    )
      return
    message.success(t('backends.messages.deleteSuccess'))
    deleteModalVisible.value = false
    deletingBackend.value = null
  } catch {
    /* Store reports a sanitized error. */
  }
}
const getBackendTypeTagType = (type: string): 'success' | 'warning' | 'info' | 'default' =>
  type === 'openai'
    ? 'success'
    : type === 'anthropic'
      ? 'warning'
      : type === 'google'
        ? 'info'
        : 'default'
const getModelDisplay = (backend: Backend) => backend.options?.model || '-'
const getBaseUrlHost = (backend: Backend) => {
  const url = backend.options?.base_url?.trim()
  if (!url) return ''
  try {
    return new URL(url).host || url
  } catch {
    return url
  }
}
const getThinkingLevelDisplay = (backend: Backend) =>
  parseThinkingLevel(backend.options?.thinking_level)
const buildCardActions = (backend: Backend): DropdownOption[] =>
  backends.canEdit(backend)
    ? [
        { label: t('common.actions.edit'), key: 'edit' },
        { label: t('backends.actions.copy'), key: 'copy' },
        { type: 'divider', key: 'divider' },
        { label: t('common.actions.delete'), key: 'delete' },
      ]
    : []
const handleCardAction = (backend: Backend, key: string | number) => {
  if (key === 'edit') openEditDrawer(backend)
  else if (key === 'copy') openCopyDrawer(backend)
  else if (key === 'delete') confirmDelete(backend)
}
const closeContext = () => {
  ++formGeneration
  drawerVisible.value = managerVisible.value = deleteModalVisible.value = false
  clearSecrets()
  editingBackend.value = deletingBackend.value = null
  credentialError.value = null
  unknownWrite.value = saving.value = false
  invalidateModelProbe()
}
watch(
  () => credentials.accessDenied,
  (denied) => {
    if (denied && drawerVisible.value && formModel.credentialMode === 'existing') {
      closeContext()
      message.error(t('configurationCredentials.denied'))
    }
  },
)
watch(orgId, closeContext, { flush: 'sync' })
onScopeDispose(onSessionChange(closeContext))
onUnmounted(closeContext)
onOrganizationInvalidated((id) => {
  if (id === orgId.value || id === editingBackend.value?.owner_org_id) closeContext()
})
useStoreErrorToast(
  () => (submitting.value || pendingBackendWrites.value > 0 ? null : backends.error),
  () => {
    backends.error = null
  },
)
</script>

<template>
  <EntityListPage
    class="lf-content-narrow"
    :title="t('backends.title')"
    :subtitle="t('backends.subtitle')"
    :loading="backends.loading"
    :empty="backends.filteredItems.length === 0"
    :empty-description="
      hasActiveFilters ? t('backends.empty.filtered') : t('backends.empty.default')
    "
  >
    <template #actions>
      <NButton secondary :loading="backends.loading" @click="backends.loadBackends(orgId)">
        {{ t('common.actions.refresh') }}
      </NButton>
      <NButton v-if="canWrite" secondary @click="openManager">{{
        t('configurationCredentials.manage')
      }}</NButton>
      <NButton v-if="canWrite" type="primary" @click="openCreateDrawer">
        {{ t('backends.create.title') }}
      </NButton>
    </template>

    <template #filters>
      <OrganizationScopeSelect :value="orgId" @update:value="setScope" />
      <NTabs
        :value="backends.typeFilter"
        type="segment"
        size="small"
        class="min-w-0"
        @update:value="
          (value: string | number) => (backends.typeFilter = value as BackendType | 'all')
        "
      >
        <NTab
          v-for="tab in filterTabs"
          :key="tab.name"
          :name="tab.name"
          :tab="() => renderFilterTab(tab)"
        />
      </NTabs>
      <NInput
        v-model:value="backends.searchQuery"
        clearable
        class="lg:max-w-sm!"
        :placeholder="t('backends.filters.searchPlaceholder')"
      />
    </template>

    <template #empty-extra>
      <NButton v-if="hasActiveFilters" secondary @click="backends.resetFilters()">
        {{ t('backends.filters.reset') }}
      </NButton>
      <NButton v-else-if="canWrite" type="primary" @click="openCreateDrawer">
        {{ t('backends.create.title') }}
      </NButton>
    </template>

    <!-- 卡片网格 -->
    <div class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <div
        v-for="backend in backends.filteredItems"
        :key="backend.id"
        class="lf-interactive-card p-5"
      >
        <div class="flex h-full flex-col gap-4">
          <div class="flex items-start justify-between gap-4">
            <div class="min-w-0">
              <h2
                class="truncate text-lg font-semibold tracking-tight text-lf-text-strong"
                :title="backend.name"
              >
                {{ backend.name }}
              </h2>
              <p class="mt-1 font-mono text-xs text-lf-text-subtle">#{{ backend.id }}</p>
            </div>
            <NTag round size="small" :bordered="false" :type="getBackendTypeTagType(backend.type)">
              {{ t(`backends.types.${backend.type}`) }}
            </NTag>
          </div>

          <div class="space-y-2.5">
            <div class="flex items-baseline gap-3">
              <span class="w-14 shrink-0 text-xs text-lf-text-subtle">
                {{ t('backends.card.model') }}
              </span>
              <span
                class="min-w-0 flex-1 truncate font-mono text-[13px] font-medium text-lf-text-strong"
                :title="getModelDisplay(backend)"
              >
                {{ getModelDisplay(backend) }}
              </span>
            </div>
            <div v-if="getThinkingLevelDisplay(backend)" class="flex items-baseline gap-3">
              <span class="w-14 shrink-0 text-xs text-lf-text-subtle">
                {{ t('backends.card.thinking') }}
              </span>
              <span class="text-[13px] text-lf-text">
                {{ t(`backends.form.thinkingLevels.${getThinkingLevelDisplay(backend)}`) }}
              </span>
            </div>
            <div v-if="getBaseUrlHost(backend)" class="flex items-baseline gap-3">
              <span class="w-14 shrink-0 text-xs text-lf-text-subtle">
                {{ t('backends.card.baseUrl') }}
              </span>
              <span
                class="min-w-0 flex-1 truncate font-mono text-[13px] text-lf-text"
                :title="getBaseUrlHost(backend)"
              >
                {{ getBaseUrlHost(backend) }}
              </span>
            </div>
            <div v-if="(backend.rate_limit_per_minute ?? 0) > 0" class="flex items-baseline gap-3">
              <span class="w-14 shrink-0 text-xs text-lf-text-subtle">
                {{ t('backends.card.rateLimit') }}
              </span>
              <span class="text-[13px] text-lf-text">
                {{ t('backends.card.rateLimitValue', { n: backend.rate_limit_per_minute }) }}
              </span>
            </div>
          </div>

          <div class="mt-auto border-t border-lf-border-soft pt-4">
            <div class="flex items-center justify-between gap-3">
              <NButton text type="primary" class="font-medium" @click="openEditDrawer(backend)">
                {{ t(backends.canEdit(backend) ? 'common.actions.edit' : 'common.actions.view') }}
              </NButton>
              <NDropdown
                v-if="backends.canEdit(backend)"
                trigger="click"
                placement="bottom-end"
                :options="buildCardActions(backend)"
                @select="(key) => handleCardAction(backend, key)"
              >
                <NButton quaternary circle size="small" :aria-label="t('common.actions.more')">
                  <template #icon>
                    <NIcon size="16">
                      <IconCarbonOverflowMenuHorizontal />
                    </NIcon>
                  </template>
                </NButton>
              </NDropdown>
            </div>
          </div>
        </div>
      </div>
    </div>
  </EntityListPage>

  <!-- 创建/编辑抽屉 -->
  <NDrawer v-model:show="drawerVisible" :width="DRAWER_WIDTH.m" placement="right">
    <NDrawerContent :native-scrollbar="false">
      <template #header>
        <DrawerHeader :title="drawerTitle" :subtitle="drawerDescription" />
      </template>

      <NForm
        ref="formRef"
        :disabled="isReadOnly || submitting"
        :model="formModel"
        :rules="rules"
        label-placement="top"
        require-mark-placement="right-hanging"
      >
        <NDivider>{{ t('configurationCredentials.basic') }}</NDivider>
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
          />
        </NFormItem>

        <NDivider />

        <NFormItem :label="t('backends.form.baseUrl')" path="base_url">
          <NInput
            v-model:value="formModel.base_url"
            :placeholder="t('backends.form.baseUrlPlaceholder')"
          />
        </NFormItem>

        <NDivider>{{ t('configurationCredentials.credential') }}</NDivider>
        <NAlert v-if="credentialError" class="mb-4" :type="unknownWrite ? 'warning' : 'error'">{{
          credentialError
        }}</NAlert>
        <NFormItem :label="t('configurationCredentials.credential')" path="credentialMode">
          <div class="w-full space-y-3">
            <p v-if="editingBackend" class="text-xs text-lf-text-muted">
              {{
                t('configurationCredentials.bound', {
                  id: editingBackend.credential.id,
                  version: editingBackend.credential.version,
                })
              }}
            </p>
            <p v-if="editingBackend" class="text-xs text-lf-text-subtle">
              {{
                t(
                  editingBackend.has_secret
                    ? 'configurationCredentials.hasSecret'
                    : 'configurationCredentials.noSecret',
                )
              }}
            </p>
            <NSelect
              v-model:value="formModel.credentialMode"
              :options="credentialModeOptions"
              :aria-label="t('configurationCredentials.credential')"
            />
            <NAlert v-if="isEditMode && !bindingUnchanged" type="warning">{{
              t('configurationCredentials.rebind')
            }}</NAlert>
            <template v-if="formModel.credentialMode === 'new'">
              <NInput
                v-model:value="formModel.secret"
                type="password"
                show-password-on="click"
                :placeholder="t('backends.form.apiKeyPlaceholder')"
                autocomplete="new-password"
                :aria-label="t('configurationCredentials.secret')"
              />
              <p class="text-xs leading-5 text-lf-text-muted">
                {{ t('configurationCredentials.newHint') }}
              </p>
            </template>
            <template v-else-if="formModel.credentialMode === 'existing' && canManageCredentials">
              <NSelect
                v-model:value="formModel.credentialId"
                :options="credentialOptions"
                :loading="credentials.loading"
                :disabled="!credentials.ready"
                filterable
                :placeholder="t('configurationCredentials.select')"
                :aria-label="t('configurationCredentials.select')"
              />
              <p class="text-xs leading-5 text-lf-text-muted">
                {{ t('configurationCredentials.selectHint') }}
              </p>
              <NAlert v-if="credentials.error" type="error">{{ credentials.error }}</NAlert>
              <NAlert v-if="credentials.stale" type="warning">{{
                t('configurationCredentials.stale')
              }}</NAlert>
              <NButton
                size="small"
                :loading="credentials.loading"
                @click="refreshCredentialChoices"
                >{{ t('configurationCredentials.refresh') }}</NButton
              >
            </template>
            <p v-else class="text-xs leading-5 text-lf-text-muted">
              {{ t('configurationCredentials.keepHint') }}
            </p>
          </div>
        </NFormItem>
        <NFormItem
          v-if="formModel.credentialMode !== 'new' && !isReadOnly"
          :label="t('configurationCredentials.probeSecret')"
        >
          <div class="w-full space-y-2">
            <NInput
              v-model:value="formModel.probeSecret"
              type="password"
              autocomplete="new-password"
              :placeholder="t('configurationCredentials.probeSecret')"
            />
            <p class="text-xs leading-5 text-lf-text-muted">
              {{ t('configurationCredentials.probeHint') }}
            </p>
          </div>
        </NFormItem>
        <NDivider>{{ t('configurationCredentials.parameters') }}</NDivider>
        <NFormItem :label="t('backends.form.model')" path="model">
          <div class="flex w-full flex-col gap-2">
            <div class="flex w-full items-center gap-2">
              <NSelect
                class="min-w-0 flex-1"
                clearable
                filterable
                tag
                :show-arrow="hasModelOptions"
                :value="formModel.model || null"
                :options="modelOptions"
                :filter="filterModelOption"
                :placeholder="t('backends.form.modelPlaceholder')"
                @update:value="(value) => (formModel.model = value == null ? '' : String(value))"
              />
              <NButton
                secondary
                :loading="fetchingModels"
                :disabled="!canFetchModels || fetchingModels"
                @click="handleFetchModels"
              >
                {{ t('backends.form.fetchModels') }}
              </NButton>
            </div>
            <p class="text-xs leading-5 text-lf-text-muted">
              {{ t('backends.form.fetchModelsHint') }}
            </p>
          </div>
        </NFormItem>

        <NFormItem :label="t('backends.form.thinkingLevel')" path="thinking_level">
          <div class="flex w-full flex-col gap-2">
            <div class="flex w-full items-center gap-3">
              <NSwitch
                v-model:value="formModel.thinkingEnabled"
                :aria-label="t('backends.form.thinkingEnabled')"
              />
              <template v-if="formModel.thinkingEnabled">
                <NSlider
                  v-model:value="thinkingLevelIndex"
                  :min="0"
                  :max="THINKING_LEVELS.length - 1"
                  :step="1"
                  :format-tooltip="formatThinkingTooltip"
                  class="min-w-0 flex-1"
                />
              </template>
              <span v-else class="text-xs text-lf-text-muted">
                {{ t('backends.form.useApiDefault') }}
              </span>
            </div>
            <p v-if="isAnthropic && isThinkingEnabled" class="text-xs leading-5 text-lf-text-muted">
              {{ t('backends.form.thinkingLevelAnthropicHint') }}
            </p>
          </div>
        </NFormItem>

        <NFormItem :label="t('backends.form.temperature')" path="temperature">
          <div class="flex w-full flex-col gap-2">
            <div class="flex w-full items-center gap-3">
              <NSwitch
                v-model:value="formModel.temperatureEnabled"
                :disabled="samplingControlsDisabled"
              />
              <template v-if="!samplingControlsDisabled && formModel.temperatureEnabled">
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
                {{
                  samplingControlsDisabled
                    ? t('backends.form.samplingIgnoredByThinking')
                    : t('backends.form.useApiDefault')
                }}
              </span>
            </div>
          </div>
        </NFormItem>

        <NFormItem :label="t('backends.form.topP')" path="top_p">
          <div class="flex w-full items-center gap-3">
            <NSwitch v-model:value="formModel.top_pEnabled" :disabled="samplingControlsDisabled" />
            <template v-if="!samplingControlsDisabled && formModel.top_pEnabled">
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
              {{
                samplingControlsDisabled
                  ? t('backends.form.samplingIgnoredByThinking')
                  : t('backends.form.useApiDefault')
              }}
            </span>
          </div>
        </NFormItem>

        <NFormItem :label="t('backends.form.maxTokens')" path="max_tokens">
          <div class="flex w-full flex-col gap-2">
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
            <p v-if="isAnthropic && isThinkingEnabled" class="text-xs leading-5 text-lf-text-muted">
              {{ t('backends.form.maxTokensThinkingHint') }}
            </p>
          </div>
        </NFormItem>

        <NFormItem :label="t('backends.form.timeout')" path="timeout">
          <div class="flex w-full items-center gap-3">
            <NSwitch v-model:value="formModel.timeoutEnabled" />
            <template v-if="formModel.timeoutEnabled">
              <NInputNumber
                v-model:value="formModel.timeout"
                :min="1"
                :placeholder="t('backends.form.timeoutPlaceholder')"
                class="flex-1"
              />
            </template>
            <span v-else class="text-xs text-lf-text-muted">
              {{ t('backends.form.timeoutUnlimited') }}
            </span>
          </div>
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

        <NDivider>{{ t('configurationCredentials.limit') }}</NDivider>
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
            {{ t('common.cancel') }}
          </NButton>
          <NButton v-if="!isReadOnly" type="primary" :loading="submitting" @click="onSubmit()">
            {{ t(unknownWrite ? 'configurationCredentials.retryWrite' : 'common.save') }}
          </NButton>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>

  <CredentialManager
    v-model:show="managerVisible"
    :org-id="orgId"
    :refresh-backends="refreshBackends"
  />

  <!-- 删除确认弹窗 -->
  <NModal
    v-model:show="deleteModalVisible"
    preset="dialog"
    type="warning"
    :title="t('common.actions.confirmDelete')"
    :content="
      deletingBackend
        ? t('configurationCredentials.deleteConfirm', { name: deletingBackend.name })
        : ''
    "
    :positive-text="t('common.actions.deleteConfirmAction')"
    :negative-text="t('common.cancel')"
    :loading="deletingBackend ? backends.deletingBackendIds.includes(deletingBackend.id) : false"
    @positive-click="executeDelete"
  />
</template>
