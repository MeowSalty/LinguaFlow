<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NCard, NEmpty, useThemeVars } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import type { ApiSchemas } from '@/api/client-core'
import { storageTaskErrorMessage } from '@/api/storage-errors'
import { formatDateTime } from '@/utils/datetime'
import { capacityTotals, formatStorageBytes } from './capacity'
import StorageCapacity from './StorageCapacity.vue'
import StorageDiskDiagnostics from './StorageDiskDiagnostics.vue'

const props = defineProps<{
  diagnostics: ApiSchemas['StorageDiagnostics']
  names: Record<number, string>
  stale?: boolean
}>()
const { t, te } = useI18n()
const themeVars = useThemeVars()
const expanded = ref<number[]>([])
const date = (value: string) => formatDateTime(value, { dateStyle: 'medium', timeStyle: 'short' })
const count = (value: number | null | undefined) =>
  typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
    ? value.toLocaleString()
    : '—'
const exactBytes = (value: number | null | undefined) =>
  typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
    ? t('storage.bytes', { value: value.toLocaleString() })
    : undefined
const rows = computed(() =>
  props.diagnostics.spaces.map((space) => ({
    ...space,
    accounted: capacityTotals(space)?.accounted,
  })),
)
const cleanup = computed(() =>
  Object.entries(props.diagnostics.blocked_cleanup_by_code ?? {}).map(([code, value]) => {
    const message = storageTaskErrorMessage(code)
    return {
      code,
      count: count(value),
      label:
        message === t('storageErrors.requestFailed') ? t('storageAdmin.unknownCleanup') : message,
    }
  }),
)
const migrations = computed(() =>
  Object.entries(props.diagnostics.migrations_by_phase ?? {}).map(([phase, value]) => ({
    phase,
    count: count(value),
    label: te(`storageAdmin.migrationPhases.${phase}`)
      ? t(`storageAdmin.migrationPhases.${phase}`)
      : t('storageAdmin.migrationUnknown'),
  })),
)
function toggle(id: number) {
  expanded.value = expanded.value.includes(id)
    ? expanded.value.filter((value) => value !== id)
    : [...expanded.value, id]
}
watch(
  () => props.diagnostics,
  () => {
    expanded.value = []
  },
)
</script>

<template>
  <div class="space-y-6">
    <NCard :title="t('storageAdmin.overviewTitle')" size="small">
      <p class="mb-5 text-sm text-lf-text-muted">{{ t('storageAdmin.overviewHint') }}</p>
      <dl class="diagnostic-summary">
        <div>
          <dt>{{ t('storageAdmin.temporary') }}</dt>
          <dd class="diagnostic-value" :title="exactBytes(diagnostics.temporary_bytes)">
            {{ formatStorageBytes(diagnostics.temporary_bytes) }}
          </dd>
        </div>
        <div>
          <dt>{{ t('storageAdmin.recoveryBacklog') }}</dt>
          <dd class="diagnostic-value">{{ count(diagnostics.recovery_backlog) }}</dd>
          <dd class="diagnostic-note">
            {{
              diagnostics.oldest_intent_at
                ? t('storageAdmin.oldestIntent', { time: date(diagnostics.oldest_intent_at) })
                : t('storageAdmin.noOldestIntent')
            }}
          </dd>
        </div>
        <div>
          <dt>{{ t('storageAdmin.latestBackup') }}</dt>
          <dd class="diagnostic-backup">
            {{
              diagnostics.latest_backup
                ? t(`storage.backupStates.${diagnostics.latest_backup.status}`)
                : t('storageAdmin.noBackup')
            }}
          </dd>
          <dd v-if="diagnostics.latest_backup" class="diagnostic-note">
            {{ date(diagnostics.latest_backup.created_at) }}
          </dd>
        </div>
      </dl>
      <div v-if="cleanup.length || migrations.length" class="diagnostic-activity">
        <section v-if="cleanup.length" class="min-w-0">
          <h3 class="mb-2 text-sm font-medium">{{ t('storageAdmin.cleanupTitle') }}</h3>
          <dl class="space-y-2">
            <div v-for="item in cleanup" :key="item.code" class="diagnostic-activity-row">
              <dt class="min-w-0 text-sm text-lf-text-muted">{{ item.label }}</dt>
              <dd class="shrink-0 tabular-nums">{{ item.count }}</dd>
            </div>
          </dl>
          <p class="mt-3 text-xs text-lf-text-muted">{{ t('storageAdmin.cleanupHint') }}</p>
        </section>
        <section v-if="migrations.length" class="min-w-0">
          <h3 class="mb-2 text-sm font-medium">{{ t('storageAdmin.migrationTitle') }}</h3>
          <dl class="space-y-2">
            <div v-for="item in migrations" :key="item.phase" class="diagnostic-activity-row">
              <dt class="min-w-0 text-sm text-lf-text-muted">{{ item.label }}</dt>
              <dd class="shrink-0 tabular-nums">{{ item.count }}</dd>
            </div>
          </dl>
          <p class="mt-3 text-xs text-lf-text-muted">{{ t('storageAdmin.migrationHint') }}</p>
        </section>
      </div>
    </NCard>

    <StorageDiskDiagnostics :disks="diagnostics.disks" :stale="stale" />

    <section aria-labelledby="diagnostic-spaces-title">
      <div class="mb-4">
        <h2 id="diagnostic-spaces-title" class="text-base font-semibold text-lf-text-strong">
          {{ t('storageAdmin.spacesTitle') }}
        </h2>
        <p class="mt-1 text-sm text-lf-text-muted">{{ t('storageAdmin.spacesHint') }}</p>
      </div>
      <NEmpty v-if="!rows.length" :description="t('storageAdmin.spacesEmpty')" class="py-8" />
      <div v-else class="diagnostic-table-frame">
        <table class="diagnostic-table">
          <thead>
            <tr>
              <th scope="col">{{ t('storageAdmin.space') }}</th>
              <th scope="col">{{ t('storageAdmin.accounted') }}</th>
              <th scope="col">{{ t('storageAdmin.observations') }}</th>
              <th scope="col">{{ t('storageAdmin.checkedAt') }}</th>
            </tr>
          </thead>
          <tbody>
            <template v-for="space in rows" :key="space.id">
              <tr class="diagnostic-space" :data-testid="`diagnostic-space-${space.id}`">
                <th scope="row" class="diagnostic-identity">
                  <button
                    type="button"
                    class="diagnostic-disclosure"
                    :aria-expanded="expanded.includes(space.id)"
                    :aria-controls="`diagnostic-details-${space.id}`"
                    :aria-label="
                      t(
                        expanded.includes(space.id)
                          ? 'storageAdmin.collapseSpace'
                          : 'storageAdmin.expandSpace',
                        { id: space.id },
                      )
                    "
                    @click="toggle(space.id)"
                  >
                    <span class="diagnostic-chevron" aria-hidden="true">{{
                      expanded.includes(space.id) ? '−' : '+'
                    }}</span>
                    <span class="min-w-0">
                      <span v-if="names[space.id]" class="block break-words font-medium">{{
                        names[space.id]
                      }}</span>
                      <span
                        :class="names[space.id] ? 'text-xs text-lf-text-muted' : 'font-medium'"
                        >{{ t('storageAdmin.spaceIdentity', { id: space.id }) }}</span
                      >
                    </span>
                  </button>
                </th>
                <td :data-label="t('storageAdmin.accounted')">
                  <span
                    class="whitespace-nowrap font-medium tabular-nums"
                    :title="exactBytes(space.accounted)"
                    >{{ formatStorageBytes(space.accounted) }}</span
                  >
                </td>
                <td :data-label="t('storageAdmin.observations')">
                  <dl class="diagnostic-counts">
                    <div>
                      <dt>{{ t('storageAdmin.unchecked') }}</dt>
                      <dd
                        :class="{
                          'font-semibold text-lf-text-strong': space.unchecked_objects > 0,
                        }"
                      >
                        {{ count(space.unchecked_objects) }}
                      </dd>
                    </div>
                    <div>
                      <dt>{{ t('storageAdmin.missing') }}</dt>
                      <dd
                        :class="{ 'font-semibold': space.missing_objects > 0 }"
                        :style="
                          space.missing_objects > 0 ? { color: themeVars.errorColor } : undefined
                        "
                      >
                        {{ count(space.missing_objects) }}
                      </dd>
                    </div>
                    <div>
                      <dt>{{ t('storageAdmin.corrupt') }}</dt>
                      <dd
                        :class="{ 'font-semibold': space.corrupt_objects > 0 }"
                        :style="
                          space.corrupt_objects > 0 ? { color: themeVars.errorColor } : undefined
                        "
                      >
                        {{ count(space.corrupt_objects) }}
                      </dd>
                    </div>
                  </dl>
                </td>
                <td :data-label="t('storageAdmin.checkedAt')" class="text-xs text-lf-text-muted">
                  {{
                    space.last_checked_at
                      ? date(space.last_checked_at)
                      : t('storageAdmin.noCheckedAt')
                  }}
                </td>
              </tr>
              <tr v-if="expanded.includes(space.id)" class="diagnostic-details">
                <td :id="`diagnostic-details-${space.id}`" colspan="4">
                  <StorageCapacity :space="space" :details-only="true" ledger-only />
                  <p class="mt-3 text-xs text-lf-text-muted">
                    {{ t('storageAdmin.observationsHint') }}
                  </p>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>
    </section>
  </div>
</template>

<style scoped>
.diagnostic-summary {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 24px;
}
.diagnostic-summary dt {
  color: var(--lf-text-muted);
  font-size: 13px;
}
.diagnostic-value {
  margin-top: 8px;
  color: var(--lf-text-strong);
  font-size: 24px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.diagnostic-backup {
  margin-top: 10px;
  color: var(--lf-text-strong);
  font-size: 17px;
  font-weight: 500;
}
.diagnostic-note {
  margin-top: 5px;
  color: var(--lf-text-muted);
  font-size: 12px;
}
.diagnostic-activity {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 260px), 1fr));
  gap: 24px;
  margin-top: 24px;
  padding-top: 20px;
  border-top: 1px solid var(--lf-border-soft);
}
.diagnostic-activity-row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 16px;
}
.diagnostic-table-frame {
  border: 1px solid var(--lf-border-soft);
  border-radius: 10px;
  overflow: hidden;
  background: var(--lf-surface);
}
.diagnostic-table {
  width: 100%;
  table-layout: fixed;
  border-collapse: collapse;
  font-size: 13px;
}
.diagnostic-table th,
.diagnostic-table td {
  padding: 16px;
  text-align: left;
  vertical-align: middle;
}
.diagnostic-table thead th {
  background: var(--lf-surface-muted);
  color: var(--lf-text-muted);
  font-size: 12px;
  font-weight: 500;
}
.diagnostic-table th:first-child {
  width: 26%;
}
.diagnostic-table th:nth-child(2) {
  width: 20%;
}
.diagnostic-table th:nth-child(3) {
  width: 31%;
}
.diagnostic-space {
  border-top: 1px solid var(--lf-border-soft);
}
.diagnostic-disclosure {
  display: flex;
  width: 100%;
  align-items: center;
  gap: 10px;
  border-radius: 8px;
  text-align: left;
  cursor: pointer;
  overflow-wrap: anywhere;
}
.diagnostic-disclosure:focus-visible {
  outline: 2px solid var(--lf-text-muted);
  outline-offset: 5px;
}
.diagnostic-chevron {
  display: grid;
  width: 22px;
  height: 22px;
  flex-shrink: 0;
  place-items: center;
  border: 1px solid var(--lf-border);
  border-radius: 6px;
  color: var(--lf-text-muted);
}
.diagnostic-counts {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 14px;
  font-size: 12px;
}
.diagnostic-counts > div {
  display: flex;
  gap: 4px;
}
.diagnostic-counts dt {
  color: var(--lf-text-muted);
}
.diagnostic-counts dd {
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.diagnostic-details td {
  padding: 20px;
  background: var(--lf-surface-muted);
  border-top: 1px solid var(--lf-border-soft);
}
@media (max-width: 767px) {
  .diagnostic-summary {
    grid-template-columns: 1fr;
    gap: 20px;
  }
  .diagnostic-summary > div + div {
    border-top: 1px solid var(--lf-border-soft);
    padding-top: 16px;
  }
  .diagnostic-value {
    font-size: 22px;
  }
  .diagnostic-table,
  .diagnostic-table tbody {
    display: block;
  }
  .diagnostic-table thead {
    display: none;
  }
  .diagnostic-space {
    display: grid;
    padding: 16px;
    gap: 12px;
  }
  .diagnostic-space:first-child {
    border-top: 0;
  }
  .diagnostic-table .diagnostic-identity {
    width: auto;
    padding: 0 0 4px;
  }
  .diagnostic-space td {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    gap: 12px;
    padding: 0;
    min-width: 0;
    text-align: right;
  }
  .diagnostic-space td::before {
    content: attr(data-label);
    flex-shrink: 0;
    color: var(--lf-text-muted);
    font-size: 12px;
  }
  .diagnostic-counts {
    justify-content: flex-end;
    gap: 4px 10px;
  }
  .diagnostic-details,
  .diagnostic-details td {
    display: block;
  }
  .diagnostic-details td {
    padding: 16px;
  }
}
</style>
