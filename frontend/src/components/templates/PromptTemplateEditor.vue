<script setup lang="ts">
import { NButton } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import HighlightTextarea from '@/components/HighlightTextarea.vue'

// ─── Props & Emits ──────────────────────────────────────────

withDefaults(
  defineProps<{
    modelValue: string
    disabled?: boolean
    rows?: number
  }>(),
  { disabled: false, rows: 6 },
)

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

// ─── 内部引用 ────────────────────────────────────────────────

const { t } = useI18n()
const editorRef = ref<InstanceType<typeof HighlightTextarea> | null>(null)

// ─── 内置变量列表 ────────────────────────────────────────────

const builtinVariables = [
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
      :placeholder="t('promptTemplates.form.contentPlaceholder')"
      :rows="rows"
      :disabled="disabled"
      @update:value="emit('update:modelValue', $event)"
    />
    <div class="mt-2 flex flex-wrap items-center gap-1.5">
      <span class="text-xs text-lf-text-muted">
        {{ t('promptTemplates.form.insertBuiltinVar') }}
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
