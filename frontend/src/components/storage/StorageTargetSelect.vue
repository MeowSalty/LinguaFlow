<script setup lang="ts">
import { computed, onUnmounted, watch } from 'vue'
import { NAlert, NButton, NEmpty, NSelect, NSkeleton } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import type { ApiSchemas } from '@/api/client-core'
import {
  createStorageTargets,
  storageTargetContextAllowed,
  watchStorageTargetContext,
  type StorageTargetContext,
} from '@/composables/useStorageTargets'
import { subscribeStorageRefresh } from '@/utils/storage-snapshots'

const props = withDefaults(
  defineProps<{ context: StorageTargetContext | null; disabled?: boolean }>(),
  { disabled: false },
)
const value = defineModel<number | null>('value', { default: null })
const emit = defineEmits<{
  loaded: [options: ApiSchemas['StorageOptions'] | null]
  selection: [option: ApiSchemas['StorageOption'] | null]
}>()
const { t, te } = useI18n()
const targets = createStorageTargets({
  context: () => props.context,
  available: () => storageTargetContextAllowed(props.context),
})
const { response, selectedId, loading, error } = targets
const allowed = computed(() => storageTargetContextAllowed(props.context))
const reasonText = (reason: string) =>
  te(`storageProject.reasons.${reason}`)
    ? t(`storageProject.reasons.${reason}`)
    : t('storageProject.reasons.unavailable')
const choices = computed(() => {
  const items = response.value?.items ?? []
  const retained = targets.retainedSelection.value
  return [
    ...items,
    ...(retained && !items.some((item) => item.space_id === retained.space_id)
      ? [{ ...retained, selectable: false, reason_codes: ['unavailable'] }]
      : []),
  ].map((item) => ({
    value: item.space_id,
    label: `${item.name} · ${t(`storageProject.scopes.${item.scope}`)}${item.reason_codes.length ? ` · ${item.reason_codes.map(reasonText).join(' / ')}` : ''}`,
    disabled: !item.selectable,
  }))
})
onUnmounted(
  watchStorageTargetContext(
    () => props.context,
    () => allowed.value,
    targets,
  ),
)
watch(
  [response, targets.status],
  ([result, status]) => emit('loaded', status === 'ready' ? result : null),
  { flush: 'sync' },
)
watch(
  [selectedId, targets.selected, targets.valid],
  ([id, selected, valid]) => {
    value.value = id
    emit('selection', valid ? selected : null)
  },
  { flush: 'sync' },
)
watch(value, (id) => {
  if (id !== selectedId.value) targets.select(id)
})
onUnmounted(targets.clear)
onUnmounted(
  subscribeStorageRefresh({
    scope: () =>
      props.context?.kind === 'project'
        ? {
            projectId: props.context.project.id,
            organizationId: props.context.project.owner_org_id,
          }
        : { organizationId: props.context?.organizationId },
    invalidate: targets.invalidate,
    refresh: targets.refresh,
  }),
)
defineExpose({ refresh: targets.refresh, clear: targets.clear })
</script>

<template>
  <div class="w-full space-y-3">
    <NAlert v-if="!allowed" type="info">{{ t('storageProject.selectionUnavailable') }}</NAlert>
    <NAlert v-if="error" type="warning">{{ error }}</NAlert>
    <NSkeleton v-if="loading && !response" height="34px" />
    <template v-else>
      <NSelect
        :value="selectedId"
        :options="choices"
        :disabled="disabled || !allowed || !response || loading || !!error"
        :placeholder="t('storageProject.chooseTarget')"
        :aria-label="t('storageProject.chooseTarget')"
        clearable
        @update:value="targets.select"
      />
      <NAlert v-if="targets.selectionUnavailable.value" type="warning">{{
        t('storageProject.retainedSelection', {
          name: targets.retainedSelection.value?.name ?? selectedId,
        })
      }}</NAlert>
      <NAlert v-if="response?.default_unavailable_reason" type="info">{{
        reasonText(response.default_unavailable_reason)
      }}</NAlert>
      <NEmpty
        v-if="response && !choices.length"
        :description="t('storageProject.noTargets')"
        size="small"
      />
      <p v-if="response" class="text-xs text-lf-text-muted">
        {{ t(`storageProject.policies.${response.policy.mode}`) }}
      </p>
    </template>
    <NButton
      size="small"
      :loading="loading"
      :disabled="disabled || !allowed"
      @click="targets.refresh"
      >{{ t('storageProject.refreshTargets') }}</NButton
    >
  </div>
</template>
