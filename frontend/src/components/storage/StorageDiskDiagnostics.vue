<script setup lang="ts">
import { computed, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ApiSchemas } from '@/api/client-core'
import { formatStorageBytes } from './capacity'
import { formatDateTime } from '@/utils/datetime'

const props = defineProps<{ disks?: ApiSchemas['StorageDiskDiagnostic'][]; stale?: boolean }>()
const { t } = useI18n()
const id = useId()
const missing = computed(() => !Array.isArray(props.disks))
const rows = computed(() =>
  (Array.isArray(props.disks) ? props.disks : []).map((disk) => ({
    roles:
      Array.isArray(disk?.roles) && disk.roles.length
        ? disk.roles
            .map((role) =>
              t(
                `storageDisk.${['work', 'metadata_or_cache', 'objects'].includes(role) ? (role as 'work' | 'metadata_or_cache' | 'objects') : 'other'}`,
              ),
            )
            .join('、')
        : t('storageDisk.other'),
    state: disk?.state === 'available' ? 'observable' : disk?.state === 'low' ? 'low' : 'unknown',
    available: disk?.available_bytes,
    total: disk?.total_bytes,
    minimum: disk?.minimum_free_bytes,
    time:
      typeof disk?.observed_at === 'string' && Number.isFinite(Date.parse(disk.observed_at))
        ? formatDateTime(disk.observed_at, { dateStyle: 'medium', timeStyle: 'short' })
        : t('storageDisk.unknownValue'),
  })),
)
const attention = computed(() => rows.value.some((row) => row.state !== 'observable'))
const bytes = (value: number | null | undefined) =>
  formatStorageBytes(value) === '—' ? t('storageDisk.unknownValue') : formatStorageBytes(value)
const exact = (value: number | null | undefined) =>
  typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
    ? t('storageCapacity.exactBytes', { value: value.toLocaleString() })
    : undefined
</script>

<template>
  <section :aria-labelledby="id" data-testid="storage-disk-diagnostics" class="min-w-0">
    <h2 :id="id" class="text-base font-semibold text-lf-text-strong">
      {{ t('storageDisk.title') }}
    </h2>
    <p class="mt-1 text-sm leading-6 text-lf-text-muted">{{ t('storageDisk.hint') }}</p>
    <NAlert v-if="attention" type="warning" class="mt-3">{{ t('storageDisk.attention') }}</NAlert>
    <p v-if="stale" class="mt-2 text-sm text-lf-text-muted" role="status">
      {{ t('storageDisk.stale') }}
    </p>
    <NEmpty
      v-if="missing || !rows.length"
      class="py-6"
      :description="t(missing ? 'storageDisk.missing' : 'storageDisk.empty')"
    />
    <div v-else class="disk-table mt-4">
      <table>
        <thead>
          <tr>
            <th
              v-for="key in [
                'roles',
                'state',
                'available',
                'total',
                'minimum',
                'observedAt',
              ] as const"
              :key="key"
              scope="col"
            >
              {{ t(`storageDisk.${key}`) }}
            </th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(row, index) in rows" :key="index">
            <th scope="row" :data-label="t('storageDisk.roles')">{{ row.roles }}</th>
            <td :data-label="t('storageDisk.state')">
              <NTag
                :type="row.state === 'low' ? 'warning' : 'default'"
                size="small"
                :bordered="false"
                >{{ t(`storageDisk.${row.state as 'observable' | 'low' | 'unknown'}`) }}</NTag
              >
            </td>
            <td
              v-for="key in ['available', 'total', 'minimum'] as const"
              :key="key"
              :data-label="t(`storageDisk.${key}`)"
              :title="exact(row[key])"
              class="tabular-nums"
            >
              {{ bytes(row[key]) }}
            </td>
            <td :data-label="t('storageDisk.observedAt')">{{ row.time }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.disk-table {
  border: 1px solid var(--lf-border-soft);
  border-radius: 10px;
  overflow: hidden;
}
table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
  text-align: left;
}
th,
td {
  padding: 12px;
  overflow-wrap: anywhere;
  vertical-align: top;
}
thead {
  background: var(--lf-bg-elevated);
  color: var(--lf-text-muted);
}
tbody tr + tr {
  border-top: 1px solid var(--lf-border-soft);
}
tbody th {
  font-weight: 500;
}
@media (max-width: 767px) {
  table,
  tbody,
  tr {
    display: block;
  }
  thead {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
  }
  tbody tr {
    padding: 8px 12px;
  }
  tbody th,
  td {
    display: flex;
    justify-content: space-between;
    gap: 16px;
    padding: 6px 0;
    text-align: right;
  }
  tbody th::before,
  td::before {
    content: attr(data-label);
    color: var(--lf-text-muted);
    text-align: left;
    flex-shrink: 0;
  }
}
</style>
