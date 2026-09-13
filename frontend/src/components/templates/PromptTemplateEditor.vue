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
    /** 编辑器占位文案，默认回退 promptTemplates.form.contentPlaceholder */
    placeholder?: string
    /** 变量插入区标签，默认回退 promptTemplates.form.insertBuiltinVar */
    insertLabel?: string
  }>(),
  {
    disabled: false,
    rows: 6,
    variableSet: 'system',
    placeholder: undefined,
    insertLabel: undefined,
  },
)

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

// ─── 内部引用 ────────────────────────────────────────────────

const { t } = useI18n()
const editorRef = ref<InstanceType<typeof HighlightTextarea> | null>(null)

// ─── 内置变量列表 ────────────────────────────────────────────

const variableGroups = {
  system: [
    'SourceLang',
    'TargetLang',
    'Protocol',
    'SourceContent',
    'TargetContent',
    'GlossaryTerms',
    'InlineBootstrap',
    'MaxBootstrapTerms',
    'RubyMode',
    'FileFormat',
    'FileName',
    'OriginalText',
    'TranslatedText',
  ],
  bootstrap: ['SourceLang', 'TargetLang', 'Protocol', 'MaxTerms'],
  prune: ['SourceLang', 'TargetLang', 'Protocol', 'Entries'],
} as const

type VariableGroup = keyof typeof variableGroups

const builtinVariables = computed(() => variableGroups[props.variableSet])

const variableLabel = (group: VariableGroup, key: string): string =>
  t(`promptTemplates.variables.${group}.${key}`)

const placeholder = computed(
  () => props.placeholder ?? t('promptTemplates.form.contentPlaceholder'),
)

const insertLabel = computed(() => props.insertLabel ?? t('promptTemplates.form.insertBuiltinVar'))

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
    <!-- 变量工具栏置于编辑器上方：任意视口高度下都可见 -->
    <div class="mb-2 flex flex-wrap items-center gap-1.5">
      <span class="text-xs text-lf-text-muted">
        {{ insertLabel }}
      </span>
      <NButton
        v-for="v in builtinVariables"
        :key="v"
        size="tiny"
        quaternary
        type="info"
        :title="variableLabel(variableSet, v)"
        :disabled="disabled"
        @click="insertVariable(v)"
      >
        {{ formatVar(v) }}
      </NButton>
    </div>
    <HighlightTextarea
      ref="editorRef"
      :value="modelValue"
      :placeholder="placeholder"
      :rows="rows"
      :disabled="disabled"
      @update:value="emit('update:modelValue', $event)"
    />
  </div>
</template>
