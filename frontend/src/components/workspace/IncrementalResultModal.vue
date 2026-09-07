<script setup lang="ts">
import { NButton, NModal } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'

type IncrementalUpdateResponse = ApiSchemas['IncrementalUpdateResponse']

defineProps<{
  show: boolean
  result: IncrementalUpdateResponse | null
}>()

const emit = defineEmits<{
  'update:show': [value: boolean]
  confirm: []
}>()

const { t } = useI18n()
</script>

<template>
  <NModal
    :show="show"
    preset="card"
    :title="t('workspace.incremental.resultTitle')"
    :style="{ width: 'min(480px, calc(100vw - 32px))' }"
    :bordered="false"
    :mask-closable="false"
    @update:show="(value: boolean) => emit('update:show', value)"
  >
    <div v-if="result" class="grid grid-cols-2 gap-3">
      <div class="rounded-lf-ctl bg-lf-success-soft p-4 text-center">
        <div class="text-2xl font-bold tabular-nums text-lf-success">
          {{ result.changes.added }}
        </div>
        <div class="mt-1 text-xs text-lf-success/70">
          {{ t('workspace.incremental.added') }}
        </div>
      </div>
      <div class="rounded-lf-ctl bg-lf-info-soft p-4 text-center">
        <div class="text-2xl font-bold tabular-nums text-lf-info">
          {{ result.changes.updated }}
        </div>
        <div class="mt-1 text-xs text-lf-info/70">
          {{ t('workspace.incremental.updated') }}
        </div>
      </div>
      <div class="rounded-lf-ctl bg-lf-surface-muted p-4 text-center">
        <div class="text-2xl font-bold tabular-nums text-lf-text-muted">
          {{ result.changes.unchanged }}
        </div>
        <div class="mt-1 text-xs text-lf-text-subtle">
          {{ t('workspace.incremental.unchanged') }}
        </div>
      </div>
      <div class="rounded-lf-ctl bg-lf-danger-soft p-4 text-center">
        <div class="text-2xl font-bold tabular-nums text-lf-danger">
          {{ result.changes.deleted }}
        </div>
        <div class="mt-1 text-xs text-lf-danger/70">
          {{ t('workspace.incremental.deleted') }}
        </div>
      </div>
    </div>
    <template #footer>
      <div class="flex justify-end">
        <NButton type="primary" @click="emit('confirm')">
          {{ t('workspace.common.confirm') }}
        </NButton>
      </div>
    </template>
  </NModal>
</template>
