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

import { type ApiSchemas } from '@/api/client'
import HighlightTextarea from '@/components/HighlightTextarea.vue'
import TranslationConfigEditor from '@/components/templates/TranslationConfigEditor.vue'
import { useTemplatesStore } from '@/stores/templates'

type Template = ApiSchemas['Template']
type CreateTemplateRequest = ApiSchemas['CreateTemplateRequest']
type UpdateTemplateRequest = ApiSchemas['UpdateTemplateRequest']
type TemplatePipelineConfig = ApiSchemas['TemplatePipelineConfig']
type TemplateGlossaryConfig = ApiSchemas['TemplateGlossaryConfig']
type TemplateScope = Template['scope']

interface TemplateFormModel {
  name: string
  description: string
  system_prompt_content: string
  pipeline: TemplatePipelineConfig
  glossary: TemplateGlossaryConfig
}

const PIPELINE_DEFAULTS: TemplatePipelineConfig = {
  split: { enabled: true, strategy: 'paragraph', max_chars: 1200 },
  protect: { enabled: true, rules: ['code', 'link', 'placeholder', 'xml'] },
  retry: { max_attempts: 3, backoff_ms: 1000, jitter: false },
  repair: {
    enabled: true,
    json_structural: true,
    schema_aliases: true,
    partial: true,
    partial_threshold: 0.5,
    placeholder_normalize: true,
    prompt_upgrade: true,
  },
  postprocess: { enabled: true, trim_spaces: true },
}

const GLOSSARY_DEFAULTS: TemplateGlossaryConfig = {
  enabled: false,
  bootstrap: {
    mode: 'off',
    save: false,
    max_terms_per_batch: 20,
    min_source_len: 2,
    inline_conflict_strategy: 'off',
  },
}

const templates = useTemplatesStore()
const message = useMessage()
const { t } = useI18n()
const formRef = ref<FormInst | null>(null)
const promptEditorRef = ref<InstanceType<typeof HighlightTextarea> | null>(null)
const drawerVisible = ref(false)
const editingTemplate = ref<Template | null>(null)
const deleteModalVisible = ref(false)
const deletingTemplate = ref<Template | null>(null)

const formModel = reactive<TemplateFormModel>({
  name: '',
  description: '',
  system_prompt_content: '',
  pipeline: JSON.parse(JSON.stringify(PIPELINE_DEFAULTS)),
  glossary: JSON.parse(JSON.stringify(GLOSSARY_DEFAULTS)),
})

const filterScopeOptions = computed<SelectOption[]>(() => [
  { label: t('templates.filters.allScopes'), value: 'all' },
  { label: t('templates.scopes.system'), value: 'system' },
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
  formModel.system_prompt_content = ''
  formModel.pipeline = JSON.parse(JSON.stringify(PIPELINE_DEFAULTS))
  formModel.glossary = JSON.parse(JSON.stringify(GLOSSARY_DEFAULTS))
  editingTemplate.value = null
}

const openCreateDrawer = (): void => {
  resetForm()
  drawerVisible.value = true
}

/** 从 API 模板对象中提取 pipeline 配置，缺失字段用默认值填充 */
function extractPipeline(template: Template): TemplatePipelineConfig {
  const src = template.pipeline
  if (!src) return JSON.parse(JSON.stringify(PIPELINE_DEFAULTS))
  return {
    split: { ...PIPELINE_DEFAULTS.split, ...src.split },
    protect: {
      ...PIPELINE_DEFAULTS.protect,
      ...src.protect,
      rules: src.protect?.rules ?? PIPELINE_DEFAULTS.protect.rules,
    },
    retry: { ...PIPELINE_DEFAULTS.retry, ...src.retry },
    repair: { ...PIPELINE_DEFAULTS.repair, ...src.repair },
    postprocess: { ...PIPELINE_DEFAULTS.postprocess, ...src.postprocess },
  }
}

/** 从 API 模板对象中提取 glossary 配置，缺失字段用默认值填充 */
function extractGlossary(template: Template): TemplateGlossaryConfig {
  const src = template.glossary
  if (!src) return JSON.parse(JSON.stringify(GLOSSARY_DEFAULTS))
  return {
    enabled: src.enabled ?? GLOSSARY_DEFAULTS.enabled,
    bootstrap: { ...GLOSSARY_DEFAULTS.bootstrap, ...src.bootstrap },
  }
}

const openEditDrawer = (template: Template): void => {
  editingTemplate.value = template
  formModel.name = template.name
  formModel.description = template.description ?? ''
  formModel.system_prompt_content = template.system_prompt_content ?? ''
  formModel.pipeline = extractPipeline(template)
  formModel.glossary = extractGlossary(template)
  drawerVisible.value = true
}

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

/** 将 {{.varName}} 插入到 system_prompt_content 的光标位置 */
const insertVariable = (varName: string): void => {
  promptEditorRef.value?.insertAtCursor(`{{.${varName}}}`)
}

const buildPayload = (): CreateTemplateRequest => {
  const payload: CreateTemplateRequest = {
    name: formModel.name.trim(),
  }

  if (formModel.description.trim()) {
    payload.description = formModel.description.trim()
  }
  if (formModel.system_prompt_content.trim()) {
    payload.system_prompt_content = formModel.system_prompt_content.trim()
  }
  if (formModel.pipeline) {
    payload.pipeline = JSON.parse(JSON.stringify(formModel.pipeline))
  }
  if (formModel.glossary) {
    payload.glossary = JSON.parse(JSON.stringify(formModel.glossary))
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
      await templates.updateTemplate(editingTemplate.value.id, payload as UpdateTemplateRequest)
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

const onCopyTemplate = async (template: Template): Promise<void> => {
  try {
    const copied = await templates.copyTemplate(template.id)
    message.success(t('templates.messages.copySuccess'))
    // 复制成功后自动打开编辑抽屉
    openEditDrawer(copied)
  } catch {
    // Error is handled by the store
  }
}

const confirmDelete = (template: Template): void => {
  if (template.scope === 'system') {
    message.warning(t('templates.messages.systemDeleteForbidden'))
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
    case 'system': {
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
          {{ t('templates.stats.system') }}
        </div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ templates.systemCount }}
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
          <!-- 头部：名称 + 作用域标签 -->
          <div class="flex items-start justify-between gap-4">
            <div class="min-w-0">
              <h2 class="truncate text-lg font-semibold text-lf-text-strong">
                {{ template.name }}
              </h2>
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
                  text
                  type="info"
                  class="font-medium"
                  :loading="templates.copying"
                  @click="onCopyTemplate(template)"
                >
                  {{ t('templates.actions.copy') }}
                </NButton>
                <NButton
                  v-if="template.scope !== 'system'"
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

          <NFormItem :label="t('templates.form.systemPromptContent')" path="system_prompt_content">
            <div class="w-full">
              <HighlightTextarea
                ref="promptEditorRef"
                v-model:value="formModel.system_prompt_content"
                :placeholder="t('templates.form.systemPromptContentPlaceholder')"
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
              </div>
            </div>
          </NFormItem>

          <!-- 翻译配置 -->
          <div class="mb-4">
            <span class="mb-2 block text-sm font-medium text-lf-text-strong">
              {{ t('templates.form.translationConfig') }}
            </span>
            <TranslationConfigEditor
              v-model:pipeline="formModel.pipeline"
              v-model:glossary="formModel.glossary"
              :disabled="editingTemplate?.scope === 'system'"
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
