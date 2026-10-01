<script setup lang="ts">
import type { OperationsSummary } from '@/api/operations'
import { useI18n } from 'vue-i18n'
const props = defineProps<{
  summary: OperationsSummary | null
  loading?: boolean
  error?: string | null
}>()
const { t } = useI18n()
const keys = ['running', 'pending', 'paused', 'recent_failed'] as const
</script>
<template>
  <div class="space-y-2">
    <div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
      <div v-for="key in keys" :key="key" class="lf-panel p-4">
        <div class="text-xs text-lf-text-muted">{{ t(`operations.${key}`) }}</div>
        <NSkeleton v-if="props.loading && !props.summary" text class="mt-3" :width="48" />
        <div v-else class="mt-2 text-2xl font-semibold tabular-nums text-lf-text-strong">
          {{ props.summary?.total[key] ?? '—' }}
        </div>
      </div>
    </div>
    <NAlert v-if="props.error" type="warning" :bordered="false"
      >{{ props.summary ? t('operations.stale') + ' · ' : '' }}{{ props.error }}</NAlert
    >
  </div>
</template>
