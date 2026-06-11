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

import { Icon as IconifyIcon } from '@iconify/vue'
import { type ApiSchemas } from '@/api/client'
import HighlightTextarea from '@/components/HighlightTextarea.vue'
import TranslationConfigEditor from '@/components/templates/TranslationConfigEditor.vue'
import { useTemplatesStore } from '@/stores/templates'

type TranslationTemplate = ApiSchemas['TranslationTemplate']
type CreateTemplatePayload = ApiSchemas['CreateTemplateRequest']
type TemplateScope = TranslationTemplate['scope']

interface TemplateFormModel {
  name: string
  description: string
  icon: string
  system_prompt: string
  prompt_vars: Array<{ key: string; value: string }>
  translation_config: Record<string, unknown>
}

const templates = useTemplatesStore()
const message = useMessage()
const { t } = useI18n()
const formRef = ref<FormInst | null>(null)
const promptEditorRef = ref<InstanceType<typeof HighlightTextarea> | null>(null)
const drawerVisible = ref(false)
const editingTemplate = ref<TranslationTemplate | null>(null)
const deleteModalVisible = ref(false)
const deletingTemplate = ref<TranslationTemplate | null>(null)

const formModel = reactive<TemplateFormModel>({
  name: '',
  description: '',
  icon: '',
  system_prompt: '',
  prompt_vars: [],
  translation_config: {},
})

const filterScopeOptions = computed<SelectOption[]>(() => [
  { label: t('templates.filters.allScopes'), value: 'all' },
  { label: t('templates.scopes.builtin'), value: 'builtin' },
  { label: t('templates.scopes.user'), value: 'user' },
  { label: t('templates.scopes.org'), value: 'org' },
])

const hasActiveFilters = computed(
  () => templates.searchQuery.trim().length > 0 || templates.scopeFilter !== 'all',
)

const isEditMode = computed(() => Boolean(editingTemplate.value))
const drawerTitle = computed(() =>
  isEditMode.value ? t('templates.actions.edit') : t('templates.actions.create'),
)

const rules = computed<FormRules>(() => ({
  name: [
    {
      required: true,
      message: t('templates.validation.nameRequired'),
      trigger: ['input', 'blur'],
    },
  ],
}))

const resetForm = (): void => {
  formModel.name = ''
  formModel.description = ''
  formModel.icon = ''
  formModel.system_prompt = ''
  formModel.prompt_vars = []
  formModel.translation_config = {}
  editingTemplate.value = null
}

const openCreateDrawer = (): void => {
  resetForm()
  drawerVisible.value = true
}

const recordToPairs = (
  record: Record<string, unknown> | undefined,
): Array<{ key: string; value: string }> => {
  if (!record) {
    return []
  }
  return Object.entries(record).map(([key, value]) => ({
    key,
    value: typeof value === 'string' ? value : JSON.stringify(value),
  }))
}

const pairsToRecord = (pairs: Array<{ key: string; value: string }>): Record<string, unknown> => {
  const record: Record<string, unknown> = {}
  for (const pair of pairs) {
    const key = pair.key.trim()
    if (key) {
      record[key] = pair.value
    }
  }
  return record
}

const openEditDrawer = (template: TranslationTemplate): void => {
  editingTemplate.value = template
  formModel.name = template.name
  formModel.description = template.description ?? ''
  formModel.icon = template.icon ?? ''
  formModel.system_prompt = template.system_prompt ?? ''
  formModel.prompt_vars = recordToPairs(template.prompt_vars)
  formModel.translation_config = template.translation_config ?? {}
  drawerVisible.value = true
}

const addPromptVar = (): void => {
  formModel.prompt_vars.push({ key: '', value: '' })
}

const removePromptVar = (index: number): void => {
  formModel.prompt_vars.splice(index, 1)
}

/** 获取已定义的变量名列表（去重、过滤空值） */
const definedVarNames = computed(() => [
  ...new Set(formModel.prompt_vars.map((v) => v.key.trim()).filter(Boolean)),
])

/** 系统内置模板变量（对应后端 prompt.Data 结构） */
const builtinVariables = [
  { key: 'SourceLang', label: '源语言', group: 'system' },
  { key: 'TargetLang', label: '目标语言', group: 'system' },
  { key: 'PrevContext', label: '前文上下文', group: 'system' },
  { key: 'NextContext', label: '后文上下文', group: 'system' },
  { key: 'Segments', label: '待翻译段落', group: 'system' },
  { key: 'Glossary', label: '术语表', group: 'system' },
  { key: 'TMHints', label: '翻译记忆提示', group: 'system' },
  { key: 'Vars.style', label: '翻译风格', group: 'vars' },
  { key: 'Vars.audience', label: '目标受众', group: 'vars' },
] as const

/** 生成变量占位符显示文本 */
const varDisplay = (varName: string): string => `{{.${varName}}}`

/** 将 {{.varName}} 插入到 system_prompt 的光标位置 */
const insertVariable = (varName: string): void => {
  promptEditorRef.value?.insertAtCursor(`{{.${varName}}}`)
}


const buildPayload = (): CreateTemplatePayload => {
  const payload: CreateTemplatePayload = {
    name: formModel.name.trim(),
  }

  if (formModel.description.trim()) {
    payload.description = formModel.description.trim()
  }
  if (formModel.icon.trim()) {
    payload.icon = formModel.icon.trim()
  }
  if (formModel.system_prompt.trim()) {
    payload.system_prompt = formModel.system_prompt.trim()
  }

  const promptVars = pairsToRecord(formModel.prompt_vars)
  if (Object.keys(promptVars).length > 0) {
    payload.prompt_vars = promptVars
  }

  if (Object.keys(formModel.translation_config).length > 0) {
    payload.translation_config = { ...formModel.translation_config }
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
    if (isEditMode.value && editingTemplate.value) {
      await templates.updateTemplate(editingTemplate.value.id, payload)
      message.success(t('templates.messages.updateSuccess'))
    } else {
      await templates.createTemplate(payload)
      message.success(t('templates.messages.createSuccess'))
    }
    drawerVisible.value = false
    resetForm()
  } catch {
    // Error is handled by the store
  }
}

const confirmDelete = (template: TranslationTemplate): void => {
  if (template.is_builtin) {
    message.warning(t('templates.messages.builtinDeleteForbidden'))
    return
  }
  deletingTemplate.value = template
  deleteModalVisible.value = true
}

const executeDelete = async (): Promise<void> => {
  if (!deletingTemplate.value) {
    return
  }

  try {
    await templates.deleteTemplate(deletingTemplate.value.id)
    message.success(t('templates.messages.deleteSuccess'))
    deleteModalVisible.value = false
    deletingTemplate.value = null
  } catch {
    // Error is handled by the store
  }
}

const getScopeTagType = (scope: TemplateScope): 'default' | 'info' | 'success' => {
  switch (scope) {
    case 'builtin': {
      return 'default'
    }
    case 'user': {
      return 'info'
    }
    case 'org': {
      return 'success'
    }
    default: {
      return 'default'
    }
  }
}

const formatDate = (dateStr: string | undefined): string => {
  if (!dateStr) {
    return t('templates.card.noDescription')
  }
  return new Date(dateStr).toLocaleDateString()
}

onMounted(() => {
  templates.loadTemplates()
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
            {{ t('templates.eyebrow') }}
          </div>
          <div>
            <h1 class="text-3xl font-semibold tracking-tight text-lf-text-strong">
              {{ t('templates.title') }}
            </h1>
            <p class="mt-2 max-w-2xl text-sm leading-6 text-lf-text-muted">
              {{ t('templates.subtitle') }}
            </p>
          </div>
        </div>
        <div class="flex flex-wrap gap-3">
          <NButton secondary :loading="templates.loading" @click="templates.loadTemplates">
            {{ t('templates.actions.refresh') }}
          </NButton>
          <NButton type="primary" @click="openCreateDrawer">
            {{ t('templates.actions.create') }}
          </NButton>
        </div>
      </div>
    </NCard>

    <!-- 统计卡片 -->
    <div class="grid grid-cols-1 gap-4 md:grid-cols-4">
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('templates.stats.total') }}
        </div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ templates.totalCount }}
        </div>
      </NCard>
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('templates.stats.builtin') }}
        </div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ templates.builtinCount }}
        </div>
      </NCard>
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('templates.stats.user') }}
        </div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ templates.userCount }}
        </div>
      </NCard>
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('templates.stats.org') }}
        </div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ templates.orgCount }}
        </div>
      </NCard>
    </div>

    <!-- 筛选栏 -->
    <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
      <div class="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <NInput
          v-model:value="templates.searchQuery"
          clearable
          class="lg:max-w-sm"
          :placeholder="t('templates.filters.searchPlaceholder')"
        />
        <div class="flex flex-wrap gap-3">
          <NSelect
            v-model:value="templates.scopeFilter"
            class="w-44"
            :options="filterScopeOptions"
          />
          <NButton
            v-if="hasActiveFilters"
            quaternary
            @click="((templates.searchQuery = ''), (templates.scopeFilter = 'all'))"
          >
            {{ t('templates.filters.reset') }}
          </NButton>
        </div>
      </div>
    </NCard>

    <!-- 错误提示 -->
    <NAlert v-if="templates.error" type="error" :bordered="false">
      {{ templates.error }}
    </NAlert>

    <!-- 加载骨架屏 -->
    <div v-if="templates.loading" class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <NCard v-for="index in 6" :key="index" :bordered="false" class="shadow-sm shadow-lf-shadow">
        <NSkeleton text :repeat="4" />
      </NCard>
    </div>

    <!-- 空状态 -->
    <NEmpty
      v-else-if="templates.filteredItems.length === 0"
      class="rounded-2xl bg-lf-surface py-16 shadow-sm shadow-lf-shadow"
      :description="hasActiveFilters ? t('templates.empty.filtered') : t('templates.empty.default')"
    >
      <template #extra>
        <NButton
          v-if="hasActiveFilters"
          secondary
          @click="((templates.searchQuery = ''), (templates.scopeFilter = 'all'))"
        >
          {{ t('templates.filters.reset') }}
        </NButton>
        <NButton v-else type="primary" @click="openCreateDrawer">
          {{ t('templates.actions.createFirst') }}
        </NButton>
      </template>
    </NEmpty>

    <!-- 模板卡片网格 -->
    <div v-else class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <NCard
        v-for="template in templates.filteredItems"
        :key="template.id"
        hoverable
        :bordered="false"
        class="group shadow-sm shadow-lf-shadow transition-all duration-200 hover:-translate-y-0.5 hover:shadow-lg hover:shadow-lf-shadow-strong"
      >
        <div class="flex h-full flex-col gap-4">
          <!-- 头部：图标 + 名称 + 作用域标签 -->
          <div class="flex items-start justify-between gap-4">
            <div class="flex min-w-0 items-center gap-3">
              <IconifyIcon
                v-if="template.icon"
                :icon="`carbon:${template.icon}`"
                class="text-2xl"
              />
              <div class="min-w-0">
                <h2 class="truncate text-lg font-semibold text-lf-text-strong">
                  {{ template.name }}
                </h2>
              </div>
            </div>
            <NTag round size="small" :type="getScopeTagType(template.scope)">
              {{ t(`templates.scopes.${template.scope}`) }}
            </NTag>
          </div>

          <!-- 描述 -->
          <p
            class="line-clamp-2 text-sm leading-6 text-lf-text-muted"
            :class="{ 'italic text-lf-text-subtle': !template.description }"
          >
            {{ template.description || t('templates.card.noDescription') }}
          </p>

          <!-- 底部：时间 + 操作 -->
          <div class="mt-auto border-t border-lf-border-soft pt-4">
            <div class="flex items-center justify-between gap-3">
              <span class="text-xs text-lf-text-subtle">
                {{ t('templates.card.createdAt') }} {{ formatDate(template.created_at) }}
              </span>
              <div class="flex items-center gap-2">
                <NButton text type="primary" class="font-medium" @click="openEditDrawer(template)">
                  {{ t('templates.actions.edit') }}
                </NButton>
                <NButton
                  v-if="!template.is_builtin"
                  text
                  type="error"
                  class="font-medium"
                  @click="confirmDelete(template)"
                >
                  {{ t('templates.actions.delete') }}
                </NButton>
              </div>
            </div>
          </div>
        </div>
      </NCard>
    </div>

    <!-- 创建/编辑抽屉 -->
    <NDrawer v-model:show="drawerVisible" :width="640" placement="right">
      <NDrawerContent :title="drawerTitle" :native-scrollbar="false">
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
          <NFormItem :label="t('templates.form.name')" path="name">
            <NInput
              v-model:value="formModel.name"
              :placeholder="t('templates.form.namePlaceholder')"
            />
          </NFormItem>

          <NFormItem :label="t('templates.form.description')" path="description">
            <NInput
              v-model:value="formModel.description"
              type="textarea"
              :placeholder="t('templates.form.descriptionPlaceholder')"
              :rows="3"
            />
          </NFormItem>

          <NFormItem :label="t('templates.form.icon')" path="icon">
            <div class="flex w-full items-center gap-2">
              <NInput
                v-model:value="formModel.icon"
                :placeholder="t('templates.form.iconPlaceholder')"
              />
              <IconifyIcon
                v-if="formModel.icon.trim()"
                :icon="`carbon:${formModel.icon.trim()}`"
                class="shrink-0 text-2xl"
              />
            </div>
          </NFormItem>

          <NFormItem :label="t('templates.form.systemPrompt')" path="system_prompt">
            <div class="w-full">
              <HighlightTextarea
                ref="promptEditorRef"
                v-model:value="formModel.system_prompt"
                :placeholder="t('templates.form.systemPromptPlaceholder')"
                :rows="6"
              />
              <div class="mt-2 space-y-2">
                <!-- 内置系统变量 -->
                <div class="flex flex-wrap items-center gap-1.5">
                  <span class="text-xs text-lf-text-muted">
                    {{ t('templates.form.insertBuiltinVar') }}
                  </span>
                  <NButton
                    v-for="v in builtinVariables"
                    :key="v.key"
                    size="tiny"
                    quaternary
                    :type="v.group === 'system' ? 'warning' : 'info'"
                    :title="v.label"
                    @click="insertVariable(v.key)"
                  >
                    {{ varDisplay(v.key) }}
                  </NButton>
                </div>
                <!-- 用户自定义变量 -->
                <div v-if="definedVarNames.length > 0" class="flex flex-wrap items-center gap-1.5">
                  <span class="text-xs text-lf-text-muted">
                    {{ t('templates.form.insertUserVar') }}
                  </span>
                  <NButton
                    v-for="varName in definedVarNames"
                    :key="varName"
                    size="tiny"
                    quaternary
                    type="primary"
                    @click="insertVariable(varName)"
                  >
                    {{ varDisplay(varName) }}
                  </NButton>
                </div>
              </div>
            </div>
          </NFormItem>

          <!-- 提示词变量 -->
          <div class="mb-4">
            <div class="mb-2 flex items-center justify-between">
              <span class="text-sm font-medium text-lf-text-strong">
                {{ t('templates.form.promptVars') }}
              </span>
              <NButton quaternary size="small" @click="addPromptVar">
                + {{ t('templates.form.addVar') }}
              </NButton>
            </div>
            <div
              v-for="(item, index) in formModel.prompt_vars"
              :key="index"
              class="mb-2 flex items-center gap-2"
            >
              <NInput
                v-model:value="item.key"
                size="small"
                :placeholder="t('templates.form.promptVarsKeyPlaceholder')"
                class="flex-1"
              />
              <NInput
                v-model:value="item.value"
                size="small"
                :placeholder="t('templates.form.promptVarsValuePlaceholder')"
                class="flex-1"
              />
              <NButton quaternary size="small" type="error" @click="removePromptVar(index)">
                ✕
              </NButton>
            </div>
          </div>

          <!-- 翻译配置 -->
          <div class="mb-4">
            <span class="mb-2 block text-sm font-medium text-lf-text-strong">
              {{ t('templates.form.translationConfig') }}
            </span>
            <TranslationConfigEditor
              v-model="formModel.translation_config"
              :disabled="editingTemplate?.is_builtin === true"
            />
          </div>
        </NForm>

        <template #footer>
          <div class="flex justify-end gap-3">
            <NButton @click="drawerVisible = false">
              {{ t('templates.actions.cancel') }}
            </NButton>
            <NButton
              type="primary"
              :loading="templates.creating || templates.updating"
              @click="onSubmit"
            >
              {{
                isEditMode
                  ? t('templates.actions.submitUpdate')
                  : t('templates.actions.submitCreate')
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
      :title="t('templates.actions.confirmDelete')"
      :content="
        deletingTemplate ? t('templates.delete.confirm', { name: deletingTemplate.name }) : ''
      "
      :positive-text="t('templates.actions.confirmDelete')"
      :negative-text="t('templates.actions.cancel')"
      :loading="
        deletingTemplate ? templates.deletingTemplateIds.includes(deletingTemplate.id) : false
      "
      @positive-click="executeDelete"
    />
  </div>
</template>
