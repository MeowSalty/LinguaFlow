<script setup lang="ts">
import { NButton } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import HighlightTextarea from '@/components/HighlightTextarea.vue'

// ─── Props & Emits ──────────────────────────────────────────

const props = withDefaults(
  defineProps<{
    modelValue: string
    disabled?: boolean
    rows?: number
    variableSet?: 'system' | 'bootstrap' | 'prune'
  }>(),
  { disabled: false, rows: 6, variableSet: 'system' },
)

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

// ─── 内部引用 ────────────────────────────────────────────────

const { t } = useI18n()
const editorRef = ref<InstanceType<typeof HighlightTextarea> | null>(null)

// ─── 内置变量列表 ────────────────────────────────────────────

const systemVariables = [
  { key: 'SourceLang', label: '源语言' },
  { key: 'TargetLang', label: '目标语言' },
  { key: 'SourceContent', label: '源内容' },
  { key: 'TargetContent', label: '目标内容' },
  { key: 'GlossaryTerms', label: '术语表' },
  { key: 'FileFormat', label: '文件格式' },
  { key: 'FileName', label: '文件名' },
  { key: 'OriginalText', label: '原始文本' },
  { key: 'TranslatedText', label: '已翻译文本' },
] as const

const bootstrapVariables = [
  { key: 'SourceLang', label: '源语言' },
  { key: 'TargetLang', label: '目标语言' },
  { key: 'MaxTerms', label: '最大术语数' },
] as const

const pruneVariables = [
  { key: 'SourceLang', label: '源语言' },
  { key: 'TargetLang', label: '目标语言' },
  { key: 'Entries', label: '完整术语条目集合' },
] as const

const builtinVariables = computed(() => {
  switch (props.variableSet) {
    case 'bootstrap':
      return bootstrapVariables
    case 'prune':
      return pruneVariables
    default:
      return systemVariables
  }
})

const placeholder = computed(() =>
  props.variableSet === 'prune'
    ? t('prunePromptTemplates.form.contentPlaceholder')
    : t('promptTemplates.form.contentPlaceholder'),
)

const insertLabel = computed(() =>
  props.variableSet === 'prune'
    ? t('prunePromptTemplates.form.insertBuiltinVar')
    : t('promptTemplates.form.insertBuiltinVar'),
)

// ─── 方法 ────────────────────────────────────────────────────

/** 格式化变量为 Go template 语法 */
function formatVar(key: string): string {
  return `{{.${key}}}`
}

const insertVariable = (varName: string): void => {
  editorRef.value?.insertAtCursor(formatVar(varName))
}
</script>

<template>
  <div class="w-full">
    <HighlightTextarea
      ref="editorRef"
      :value="modelValue"
      :placeholder="placeholder"
      :rows="rows"
      :disabled="disabled"
      @update:value="emit('update:modelValue', $event)"
    />
    <div class="mt-2 flex flex-wrap items-center gap-1.5">
      <span class="text-xs text-lf-text-muted">
        {{ insertLabel }}
      </span>
      <NButton
        v-for="v in builtinVariables"
        :key="v.key"
        size="tiny"
        quaternary
        type="info"
        :title="v.label"
        :disabled="disabled"
        @click="insertVariable(v.key)"
      >
        {{ formatVar(v.key) }}
      </NButton>
    </div>
  </div>
</template>
