<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton, NCheckbox, NRadioButton, NRadioGroup, NTag } from 'naive-ui'

import type { PendingUploadItem, PendingUploadStrategy } from '@/stores/projectWorkspace'

const props = defineProps<{
  items: PendingUploadItem[]
  loading?: boolean
}>()

const emit = defineEmits<{
  confirm: []
  cancel: []
  updateSelected: [itemId: string, selected: boolean]
  updateStrategy: [itemId: string, strategy: PendingUploadStrategy]
  updateAllCreatable: [selected: boolean]
}>()

const { t } = useI18n()

const creatableItems = computed(() =>
  props.items.filter((item) => item.precheck.action === 'create'),
)
const selectedCount = computed(() => props.items.filter((item) => item.strategy !== 'skip').length)
const createCount = computed(() => props.items.filter((item) => item.strategy === 'create').length)
const conflictCount = computed(
  () => props.items.filter((item) => item.precheck.action === 'conflict').length,
)
const incrementalUpdateCount = computed(
  () => props.items.filter((item) => item.strategy === 'incremental_update').length,
)
const replaceCount = computed(
  () => props.items.filter((item) => item.strategy === 'replace').length,
)
const duplicateCount = computed(
  () => props.items.filter((item) => item.precheck.action === 'duplicate').length,
)
const allCreatableSelected = computed(
  () => creatableItems.value.length > 0 && creatableItems.value.every((item) => item.selected),
)
const partiallyCreatableSelected = computed(
  () => creatableItems.value.some((item) => item.selected) && !allCreatableSelected.value,
)
const hasProblemItems = computed(() => conflictCount.value > 0 || duplicateCount.value > 0)

const actionTagType = (
  action: PendingUploadItem['precheck']['action'],
): 'success' | 'warning' | 'error' => {
  if (action === 'create') {
    return 'success'
  }
  if (action === 'conflict') {
    return 'warning'
  }
  return 'error'
}

const itemToneClass = (item: PendingUploadItem): string => {
  if (item.strategy === 'skip') {
    return 'border-lf-border bg-lf-surface-muted/40'
  }
  if (item.precheck.action === 'create') {
    return 'border-emerald-200 bg-emerald-50/40 dark:border-emerald-500/20 dark:bg-emerald-500/5'
  }
  if (item.precheck.action === 'conflict') {
    return 'border-amber-200 bg-amber-50/40 dark:border-amber-500/20 dark:bg-amber-500/5'
  }
  return 'border-red-200 bg-red-50/40 dark:border-red-500/20 dark:bg-red-500/5'
}

const getReason = (item: PendingUploadItem): string => {
  if (item.precheck.action === 'create') {
    return t('workspace.uploadPrecheck.reasons.create')
  }
  if (item.precheck.action === 'conflict') {
    return t('workspace.uploadPrecheck.reasons.conflict', {
      name: item.precheck.existing_resource?.name ?? item.path,
    })
  }
  return t('workspace.uploadPrecheck.reasons.duplicate')
}

const getResolutionHint = (item: PendingUploadItem): string => {
  if (item.precheck.action === 'create') {
    return t('workspace.uploadPrecheck.strategies.createHint')
  }
  if (item.precheck.action === 'duplicate') {
    return t('workspace.uploadPrecheck.strategies.skipHint')
  }
  if (item.strategy === 'replace') {
    return t('workspace.uploadPrecheck.strategies.replaceHint')
  }
  if (item.strategy === 'skip') {
    return t('workspace.uploadPrecheck.strategies.skipHint')
  }
  return t('workspace.uploadPrecheck.strategies.incrementalUpdateHint')
}
</script>

<template>
  <div class="space-y-5">
    <div class="rounded-2xl border border-lf-border bg-lf-surface-muted/70 p-4 shadow-sm">
      <div class="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
        <div class="min-w-0">
          <div class="text-base font-semibold text-lf-text-strong">
            {{ t('workspace.uploadPrecheck.title') }}
          </div>
          <p class="mt-1 max-w-2xl text-sm leading-6 text-lf-text-muted">
            {{
              hasProblemItems
                ? t('workspace.uploadPrecheck.descriptionWithProblems')
                : t('workspace.uploadPrecheck.descriptionReady')
            }}
          </p>
        </div>
        <div class="grid shrink-0 grid-cols-3 gap-2 text-center sm:grid-cols-5">
          <div class="rounded-xl bg-emerald-50 px-3 py-2 dark:bg-emerald-500/10">
            <div class="text-lg font-bold text-emerald-600">{{ createCount }}</div>
            <div class="text-xs text-emerald-700 dark:text-emerald-300">
              {{ t('workspace.uploadPrecheck.summary.creatable') }}
            </div>
          </div>
          <div class="rounded-xl bg-amber-50 px-3 py-2 dark:bg-amber-500/10">
            <div class="text-lg font-bold text-amber-600">{{ conflictCount }}</div>
            <div class="text-xs text-amber-700 dark:text-amber-300">
              {{ t('workspace.uploadPrecheck.summary.conflicts') }}
            </div>
          </div>
          <div class="rounded-xl bg-blue-50 px-3 py-2 dark:bg-blue-500/10">
            <div class="text-lg font-bold text-blue-600">{{ incrementalUpdateCount }}</div>
            <div class="text-xs text-blue-700 dark:text-blue-300">
              {{ t('workspace.uploadPrecheck.summary.incrementalUpdates') }}
            </div>
          </div>
          <div class="rounded-xl bg-purple-50 px-3 py-2 dark:bg-purple-500/10">
            <div class="text-lg font-bold text-purple-600">{{ replaceCount }}</div>
            <div class="text-xs text-purple-700 dark:text-purple-300">
              {{ t('workspace.uploadPrecheck.summary.replaces') }}
            </div>
          </div>
          <div class="rounded-xl bg-red-50 px-3 py-2 dark:bg-red-500/10">
            <div class="text-lg font-bold text-red-600">{{ duplicateCount }}</div>
            <div class="text-xs text-red-700 dark:text-red-300">
              {{ t('workspace.uploadPrecheck.summary.duplicates') }}
            </div>
          </div>
        </div>
      </div>

      <div
        v-if="creatableItems.length > 0"
        class="mt-4 flex flex-col gap-3 rounded-xl border border-lf-border bg-lf-surface px-3 py-2 sm:flex-row sm:items-center sm:justify-between"
      >
        <div class="text-sm text-lf-text-muted">
          {{ t('workspace.uploadPrecheck.columns.selectAllCreatable') }}
        </div>
        <NCheckbox
          :checked="allCreatableSelected"
          :indeterminate="partiallyCreatableSelected"
          @update:checked="(checked) => emit('updateAllCreatable', checked)"
        >
          {{ t('workspace.uploadPrecheck.strategies.create') }}
        </NCheckbox>
      </div>
    </div>

    <div class="max-h-[52vh] space-y-3 overflow-y-auto pr-1">
      <div
        v-for="item in props.items"
        :key="item.id"
        class="rounded-2xl border p-4 transition-colors"
        :class="itemToneClass(item)"
      >
        <div class="grid gap-4 lg:grid-cols-[minmax(0,1fr)_340px] lg:items-start">
          <div class="min-w-0">
            <div class="flex items-start gap-3">
              <div class="pt-0.5">
                <NCheckbox
                  v-if="item.precheck.action === 'create'"
                  :checked="item.selected"
                  @update:checked="(checked) => emit('updateSelected', item.id, checked)"
                />
                <div
                  v-else
                  class="flex h-5 w-5 items-center justify-center rounded-full border border-lf-border bg-lf-surface text-[10px] text-lf-text-muted"
                >
                  —
                </div>
              </div>
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center gap-2">
                  <div class="min-w-0 truncate text-sm font-semibold text-lf-text-strong">
                    {{ item.path }}
                  </div>
                  <NTag :type="actionTagType(item.precheck.action)" size="small" :bordered="false">
                    {{ t(`workspace.uploadPrecheck.actions.${item.precheck.action}`) }}
                  </NTag>
                </div>
                <p class="mt-1 line-clamp-2 text-xs leading-5 text-lf-text-muted">
                  {{ getReason(item) }}
                </p>
              </div>
            </div>
          </div>

          <div class="rounded-xl border border-lf-border/70 bg-lf-surface/80 p-3">
            <template v-if="item.precheck.action === 'create'">
              <div class="flex items-center justify-between gap-3">
                <span class="text-xs font-medium text-lf-text-muted">
                  {{ t('workspace.uploadPrecheck.columns.conflictResolution') }}
                </span>
                <NTag :type="item.selected ? 'success' : 'default'" size="small" :bordered="false">
                  {{
                    item.selected
                      ? t('workspace.uploadPrecheck.strategies.create')
                      : t('workspace.uploadPrecheck.strategies.skip')
                  }}
                </NTag>
              </div>
            </template>

            <template
              v-else-if="item.precheck.action === 'conflict' && item.precheck.existing_resource"
            >
              <NRadioGroup
                :value="item.strategy"
                size="small"
                @update:value="
                  (value) => emit('updateStrategy', item.id, value as PendingUploadStrategy)
                "
              >
                <div
                  class="grid grid-cols-3 overflow-hidden rounded-lg border border-lf-border bg-lf-surface"
                >
                  <NRadioButton value="incremental_update" class="text-center">
                    {{ t('workspace.uploadPrecheck.strategies.incrementalUpdate') }}
                  </NRadioButton>
                  <NRadioButton value="replace" class="text-center">
                    {{ t('workspace.uploadPrecheck.strategies.replace') }}
                  </NRadioButton>
                  <NRadioButton value="skip" class="text-center">
                    {{ t('workspace.uploadPrecheck.strategies.skip') }}
                  </NRadioButton>
                </div>
              </NRadioGroup>
            </template>

            <template v-else>
              <div class="flex items-center justify-between gap-3">
                <span class="text-xs font-medium text-lf-text-muted">
                  {{ t('workspace.uploadPrecheck.columns.conflictResolution') }}
                </span>
                <NTag type="default" size="small" :bordered="false">
                  {{ t('workspace.uploadPrecheck.strategies.skip') }}
                </NTag>
              </div>
            </template>

            <p class="mt-2 text-xs leading-5 text-lf-text-muted">
              {{ getResolutionHint(item) }}
            </p>
          </div>
        </div>
      </div>
    </div>

    <div
      class="flex flex-col gap-3 border-t border-lf-border pt-4 sm:flex-row sm:items-center sm:justify-between"
    >
      <p class="text-sm text-lf-text-muted">
        {{ t('workspace.uploadPrecheck.selectedHint', { count: selectedCount }) }}
      </p>
      <div class="flex justify-end gap-2">
        <NButton :disabled="loading" @click="emit('cancel')">
          {{ t('workspace.common.cancel') }}
        </NButton>
        <NButton
          type="primary"
          :disabled="selectedCount === 0"
          :loading="loading"
          @click="emit('confirm')"
        >
          {{ t('workspace.uploadPrecheck.confirmSelected', { count: selectedCount }) }}
        </NButton>
      </div>
    </div>
  </div>
</template>
