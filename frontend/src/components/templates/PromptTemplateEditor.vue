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
  </div>
</template>
