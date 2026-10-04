<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { capacityTotals, type CapacityLedger } from './capacity'
const props = defineProps<{ space: CapacityLedger }>()
const { t } = useI18n()
const totals = computed(() => capacityTotals(props.space))
const rows = computed(() => [
  ...(props.space.capacity_bytes === undefined
    ? []
    : [{ label: 'storage.capacity', value: props.space.capacity_bytes }]),
  { label: 'storage.reserved', value: props.space.reserved_bytes },
  { label: 'storage.candidate', value: props.space.candidate_bytes },
  { label: 'storage.live', value: props.space.live_bytes },
  { label: 'storage.pendingDelete', value: props.space.pending_delete_bytes },
  { label: 'storageManagement.accounted', value: totals.value?.accounted },
  ...(props.space.capacity_bytes === undefined
    ? []
    : [{ label: 'storageManagement.available', value: totals.value?.available }]),
])
</script>
<template>
  <div>
    <dl class="grid grid-cols-2 gap-3 sm:grid-cols-3">
      <div v-for="row in rows" :key="row.label" class="rounded-lg bg-lf-surface-muted p-3">
        <dt class="text-xs text-lf-text-muted">{{ t(row.label) }}</dt>
        <dd class="mt-1 break-all font-medium tabular-nums">
          {{
            typeof row.value === 'number' && Number.isSafeInteger(row.value) && row.value >= 0
              ? t('storage.bytes', { value: row.value.toLocaleString() })
              : t('storage.unknown')
          }}
        </dd>
      </div>
    </dl>
    <p class="mt-3 text-xs text-lf-text-subtle">{{ t('storageManagement.capacityHint') }}</p>
  </div>
</template>
