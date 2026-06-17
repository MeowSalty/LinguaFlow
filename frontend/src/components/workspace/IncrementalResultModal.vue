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
    :style="{ width: '480px' }"
    :bordered="false"
    :mask-closable="false"
    @update:show="(value: boolean) => emit('update:show', value)"
  >
    <div v-if="result" class="grid grid-cols-2 gap-3">
      <div class="rounded-lg bg-emerald-50 p-4 text-center dark:bg-emerald-500/10">
        <div class="text-2xl font-bold text-emerald-600">
          {{ result.changes.added }}
        </div>
        <div class="mt-1 text-xs text-emerald-600/70">
          {{ t('workspace.incremental.added') }}
        </div>
      </div>
      <div class="rounded-lg bg-blue-50 p-4 text-center dark:bg-blue-500/10">
        <div class="text-2xl font-bold text-blue-600">
          {{ result.changes.updated }}
        </div>
        <div class="mt-1 text-xs text-blue-600/70">
          {{ t('workspace.incremental.updated') }}
        </div>
      </div>
      <div class="rounded-lg bg-gray-50 p-4 text-center dark:bg-gray-500/10">
        <div class="text-2xl font-bold text-gray-600">
          {{ result.changes.unchanged }}
        </div>
        <div class="mt-1 text-xs text-gray-600/70">
          {{ t('workspace.incremental.unchanged') }}
        </div>
      </div>
      <div class="rounded-lg bg-red-50 p-4 text-center dark:bg-red-500/10">
        <div class="text-2xl font-bold text-red-600">
          {{ result.changes.deleted }}
        </div>
        <div class="mt-1 text-xs text-red-600/70">
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
