<script setup lang="ts">
import type { FormInst, FormRules } from 'naive-ui'
import {
  NAlert,
  NButton,
  NCheckbox,
  NDrawer,
  NDrawerContent,
  NForm,
  NFormItem,
  NInput,
  NSwitch,
  NTag,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { GlossaryFormModel } from '@/composables/useGlossaryManagement'

const { t } = useI18n()

const show = defineModel<boolean>('show', { default: false })

const props = defineProps<{
  isEditMode: boolean
  drawerTitle: string
  formRef: FormInst | null
  form: GlossaryFormModel
  formRules: FormRules
  submitting: boolean
  error: string | null
}>()

const emit = defineEmits<{
  submit: []
  close: []
  'update:formSource': [value: string]
  'update:formTarget': [value: string]
  'update:formForbidden': [value: boolean]
  'update:formMandatory': [value: boolean]
  'update:formCaseSensitive': [value: boolean]
  'update:formNotes': [value: string]
}>()

const notePresetGroupDefs = [
  {
    group: 'type',
    keys: [
      'personName',
      'maleName',
      'femaleName',
      'surname',
      'characterName',
      'formOfAddress',
      'placeName',
      'orgName',
      'brandName',
      'productName',
      'workTitle',
      'acronym',
    ],
  },
  {
    group: 'handling',
    keys: [
      'keepOriginal',
      'keepEnglish',
      'doNotTranslate',
      'transliterate',
      'freeTranslate',
      'fixedTranslation',
      'allCaps',
      'capitalize',
    ],
  },
] as const

const notePresetGroups = computed(() =>
  notePresetGroupDefs.map(({ group, keys }) => ({
    label: t(`workspace.glossary.form.notesPresetGroups.${group}`),
    presets: keys.map((key) => t(`workspace.glossary.form.notesPresets.${group}.${key}`)),
  })),
)

const noteSegments = computed(() =>
  props.form.notes
    .split(/[;；]/)
    .map((segment) => segment.trim())
    .filter(Boolean),
)

const isNotePresetActive = (preset: string): boolean => noteSegments.value.includes(preset)

const toggleNotePreset = (preset: string): void => {
  const segments = noteSegments.value
  const next = segments.includes(preset)
    ? segments.filter((segment) => segment !== preset)
    : [...segments, preset]
  emit('update:formNotes', next.join('；'))
}
</script>

<template>
  <NDrawer v-model:show="show" :width="'min(480px, 100vw)'" placement="right">
    <NDrawerContent :title="drawerTitle" closable>
      <NAlert v-if="error" type="error" :bordered="false" class="mb-4">
        {{ error }}
      </NAlert>
      <NForm ref="formRef" :model="form" :rules="formRules" label-placement="top">
        <NFormItem :label="t('workspace.glossary.form.source')" path="source">
          <NInput
            :value="form.source"
            :placeholder="t('workspace.glossary.form.sourcePlaceholder')"
            @update:value="(val: string) => emit('update:formSource', val)"
          />
        </NFormItem>
        <NFormItem :label="t('workspace.glossary.form.target')" path="target">
          <NInput
            :value="form.target"
            :placeholder="t('workspace.glossary.form.targetPlaceholder')"
            @update:value="(val: string) => emit('update:formTarget', val)"
          />
        </NFormItem>
        <NFormItem :label="t('workspace.glossary.form.caseSensitive')">
          <NCheckbox
            :checked="form.case_sensitive"
            @update:checked="(val: boolean) => emit('update:formCaseSensitive', val)"
          >
            {{ t('workspace.glossary.form.caseSensitive') }}
          </NCheckbox>
        </NFormItem>
        <NFormItem :label="t('workspace.glossary.form.forbidden')">
          <div class="flex w-full items-center justify-between">
            <span class="text-xs text-lf-text-subtle">{{
              t('workspace.glossary.form.forbiddenHint')
            }}</span>
            <NSwitch
              :value="form.forbidden"
              size="small"
              @update:value="(val: boolean) => emit('update:formForbidden', val)"
            />
          </div>
        </NFormItem>
        <NFormItem :label="t('workspace.glossary.form.mandatory')">
          <div class="flex w-full items-center justify-between">
            <span class="text-xs text-lf-text-subtle">{{
              t('workspace.glossary.form.mandatoryHint')
            }}</span>
            <NSwitch
              :value="form.mandatory"
              size="small"
              :disabled="form.forbidden"
              @update:value="(val: boolean) => emit('update:formMandatory', val)"
            />
          </div>
        </NFormItem>
        <NFormItem :label="t('workspace.glossary.form.notes')">
          <div class="w-full space-y-2">
            <NInput
              :value="form.notes"
              type="textarea"
              :autosize="{ minRows: 2, maxRows: 4 }"
              :placeholder="t('workspace.glossary.form.notesPlaceholder')"
              @update:value="(val: string) => emit('update:formNotes', val)"
            />
            <div class="space-y-1.5">
              <div
                v-for="group in notePresetGroups"
                :key="group.label"
                class="flex flex-wrap items-center gap-1.5"
              >
                <span class="text-xs text-lf-text-subtle">{{ group.label }}</span>
                <NTag
                  v-for="preset in group.presets"
                  :key="preset"
                  checkable
                  size="small"
                  :checked="isNotePresetActive(preset)"
                  @update:checked="() => toggleNotePreset(preset)"
                >
                  {{ preset }}
                </NTag>
              </div>
            </div>
          </div>
        </NFormItem>
      </NForm>
      <template #footer>
        <div class="flex justify-end gap-3">
          <NButton :disabled="submitting" @click="emit('close')">
            {{ t('workspace.common.cancel') }}
          </NButton>
          <NButton type="primary" :loading="submitting" @click="emit('submit')">
            {{ t('workspace.common.save') }}
          </NButton>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
