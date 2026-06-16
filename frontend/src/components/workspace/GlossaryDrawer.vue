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
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { GlossaryFormModel } from '@/composables/useGlossaryManagement'

const { t } = useI18n()

const show = defineModel<boolean>('show', { default: false })

defineProps<{
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
  'update:formCaseSensitive': [value: boolean]
  'update:formNotes': [value: string]
}>()
</script>

<template>
  <NDrawer v-model:show="show" :width="480" placement="right">
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
        <NFormItem :label="t('workspace.glossary.form.notes')">
          <NInput
            :value="form.notes"
            type="textarea"
            :autosize="{ minRows: 2, maxRows: 4 }"
            :placeholder="t('workspace.glossary.form.notesPlaceholder')"
            @update:value="(val: string) => emit('update:formNotes', val)"
          />
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
