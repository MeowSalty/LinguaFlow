<script setup lang="ts">
import { computed } from 'vue'
import { useThemeVars } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import {
  capacityTotals,
  formatStorageBytes,
  formatStorageQuota,
  type CapacityLedger,
} from './capacity'

const props = defineProps<{
  space: CapacityLedger
  compact?: boolean
  detailsOnly?: boolean
  ledgerOnly?: boolean
}>()
const { t } = useI18n()
const themeVars = useThemeVars()
const totals = computed(() => capacityTotals(props.space))
const quotaKnown = computed(() => totals.value && totals.value.quotaState !== 'unknown')
const quotaLabel = computed(() =>
  quotaKnown.value
    ? formatStorageQuota(props.space.capacity_bytes, t('storageCapacity.unlimited'))
    : t('storageCapacity.unknown'),
)
const percentage = computed(() => {
  const capacity = props.space.capacity_bytes
  return !props.ledgerOnly &&
    totals.value?.quotaState === 'finite' &&
    typeof capacity === 'number' &&
    Number.isSafeInteger(capacity) &&
    capacity > 0
    ? (totals.value.accounted / capacity) * 100
    : null
})
const rows = computed(() => [
  { label: 'storageCapacity.reserved', value: props.space.reserved_bytes },
  { label: 'storageCapacity.candidate', value: props.space.candidate_bytes },
  { label: 'storageCapacity.live', value: props.space.live_bytes },
  { label: 'storageCapacity.pendingDelete', value: props.space.pending_delete_bytes },
  { label: 'storageCapacity.accounted', value: totals.value?.accounted },
  ...(!props.ledgerOnly
    ? [
        {
          label: 'storageCapacity.quota',
          value: props.space.capacity_bytes,
          text: quotaLabel.value,
        },
        {
          label: 'storageCapacity.available',
          value: totals.value?.available,
          text:
            totals.value?.quotaState === 'unlimited'
              ? t('storageCapacity.unlimited')
              : formatStorageBytes(totals.value?.available),
        },
      ]
    : []),
])
function exactBytes(value: number | undefined | null) {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
    ? t('storageCapacity.exactBytes', { value: value.toLocaleString() })
    : t('storageCapacity.unknown')
}
</script>

<template>
  <div class="min-w-0 text-sm">
    <template v-if="!detailsOnly">
      <div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <span class="text-xs text-lf-text-muted">{{
          t(ledgerOnly ? 'storageCapacity.accounted' : 'storageCapacity.summary')
        }}</span>
        <span class="inline-flex flex-wrap gap-x-1.5 font-medium tabular-nums">
          <span class="whitespace-nowrap">{{ formatStorageBytes(totals?.accounted) }}</span>
          <template v-if="!ledgerOnly">
            <span class="text-lf-text-muted">/</span>
            <span>{{ quotaLabel }}</span>
          </template>
        </span>
      </div>
      <div
        v-if="percentage !== null"
        class="mt-2 h-1.5 overflow-hidden rounded-full bg-lf-border-soft"
        role="progressbar"
        :aria-label="t('storageCapacity.summary')"
        :aria-valuenow="Math.min(percentage, 100)"
        :aria-valuemin="0"
        :aria-valuemax="100"
        :aria-valuetext="`${exactBytes(totals?.accounted)} / ${exactBytes(space.capacity_bytes)}`"
      >
        <div
          class="h-full rounded-full"
          :class="percentage > 100 ? 'bg-lf-danger' : 'bg-brand-500'"
          :style="{ width: `${Math.min(percentage, 100)}%` }"
        />
      </div>
      <p
        v-if="percentage !== null && percentage > 100"
        class="mt-1 text-xs"
        :style="{ color: themeVars.errorColor }"
      >
        {{ t('storageCapacity.exceededBy', { value: formatStorageBytes(totals?.exceeded) }) }}
      </p>
      <p v-if="!ledgerOnly && !quotaKnown" class="mt-1 text-xs text-lf-text-muted">
        {{ t('storageCapacity.unavailable') }}
      </p>
    </template>
    <details v-if="!compact" :open="detailsOnly" :class="{ 'mt-3': !detailsOnly }">
      <summary class="w-fit cursor-pointer text-xs text-lf-text-muted">
        {{ t('storageCapacity.details') }}
      </summary>
      <dl class="mt-3 space-y-2">
        <div
          v-for="row in rows"
          :key="row.label"
          class="flex flex-wrap justify-between gap-x-4 gap-y-0.5"
        >
          <dt class="text-lf-text-muted">{{ t(row.label) }}</dt>
          <dd class="ml-auto text-right tabular-nums">
            <span class="font-medium">{{
              'text' in row ? row.text : formatStorageBytes(row.value)
            }}</span>
            <span
              v-if="
                formatStorageBytes(row.value) !== '—' &&
                (ledgerOnly ||
                  !['storageCapacity.quota', 'storageCapacity.available'].includes(row.label) ||
                  quotaKnown)
              "
              class="ml-2 inline-block break-words text-xs text-lf-text-muted"
            >
              {{ exactBytes(row.value) }}
            </span>
          </dd>
        </div>
      </dl>
      <p class="mt-3 text-xs leading-relaxed text-lf-text-muted">{{ t('storageCapacity.hint') }}</p>
    </details>
  </div>
</template>
