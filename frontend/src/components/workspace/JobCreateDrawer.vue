<script setup lang="ts">
import type { FormInst, FormRules } from 'naive-ui'
import { NButton, NDrawer, NDrawerContent, NForm, NFormItem, NSelect } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { useExecutionPlanTemplatesStore } from '@/stores/executionPlanTemplates'

import type { JobTargetMode } from '@/composables/useJobManagement'

const { t } = useI18n()
const executionPlanTemplatesStore = useExecutionPlanTemplatesStore()

const show = defineModel<boolean>('show', { default: false })

defineProps<{
  formRef: FormInst | null
  targetMode: JobTargetMode
  targetResourceIds: number[]
  targetSegmentIds: number[]
  executionPlanId: number | null
  formRules: FormRules
  executionPlanOptions: Array<{ label: string; value: number }>
  submitting: boolean
}>()

const emit = defineEmits<{
  'update:executionPlanId': [value: number | null]
  submit: []
  close: []
}>()
</script>

<template>
  <NDrawer v-model:show="show" :width="480" placement="right">
    <NDrawerContent :title="t('workspace.job.createTitle')" closable>
      <NForm
        ref="formRef"
        :model="{ execution_plan_id: executionPlanId }"
        :rules="formRules"
        label-placement="top"
      >
        <NFormItem :label="t('workspace.job.form.executionPlan')" path="execution_plan_id">
          <NSelect
            :value="executionPlanId"
            :options="executionPlanOptions"
            :loading="executionPlanTemplatesStore.loading"
            :placeholder="t('workspace.job.form.executionPlanPlaceholder')"
            filterable
            @update:value="(val: number | null) => emit('update:executionPlanId', val)"
          />
        </NFormItem>

        <div v-if="targetMode === 'resources'" class="text-sm text-lf-text-muted">
          {{ t('workspace.job.targetResources', { count: targetResourceIds.length }) }}
        </div>
        <div v-else class="text-sm text-lf-text-muted">
          {{ t('workspace.job.targetSegments', { count: targetSegmentIds.length }) }}
        </div>
      </NForm>

      <template #footer>
        <div class="flex justify-end gap-3">
          <NButton :disabled="submitting" @click="emit('close')">
            {{ t('workspace.common.cancel') }}
          </NButton>
          <NButton
            type="primary"
            :disabled="!executionPlanId"
            :loading="submitting"
            @click="emit('submit')"
          >
            {{ t('workspace.job.actions.submitCreate') }}
          </NButton>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
