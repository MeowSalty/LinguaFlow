<script setup lang="ts">
import { computed, onUnmounted, watch } from 'vue'
import { NAlert, NButton, NEmpty, NSelect, NSkeleton } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import type { ApiSchemas } from '@/api/client-core'
import { sessionGeneration } from '@/api/session-context'
import {
  createStorageTargets,
  storageTargetContextAllowed,
  storageTargetContextKey,
  type StorageTargetContext,
} from '@/composables/useStorageTargets'

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
const choices = computed(
  () =>
    response.value?.items.map((item) => ({
      value: item.space_id,
      label: `${item.name} · ${t(`storageProject.scopes.${item.scope}`)}${item.reason_codes.length ? ` · ${item.reason_codes.map(reasonText).join(' / ')}` : ''}`,
      disabled: !item.selectable,
    })) ?? [],
)
watch(
  () => [storageTargetContextKey(props.context), allowed.value, sessionGeneration.value],
  () => {
    targets.clear()
    void targets.refresh()
  },
  { immediate: true, flush: 'sync' },
)
watch(response, (result) => emit('loaded', result), { flush: 'sync' })
watch(
  selectedId,
  (id) => {
    value.value = id
    emit('selection', targets.selected.value)
  },
  { flush: 'sync' },
)
watch(value, (id) => {
  if (id !== selectedId.value) targets.select(id)
})
onUnmounted(targets.clear)
defineExpose({ refresh: targets.refresh, clear: targets.clear })
</script>

<template>
  <div class="w-full space-y-3">
    <NAlert v-if="!allowed" type="info">{{ t('storageProject.selectionUnavailable') }}</NAlert>
    <NAlert v-if="error" type="warning">{{ error }}</NAlert>
    <NSkeleton v-if="loading" height="34px" />
    <template v-else>
      <NSelect
        :value="selectedId"
        :options="choices"
        :disabled="disabled || !allowed || !response"
        :placeholder="t('storageProject.chooseTarget')"
        :aria-label="t('storageProject.chooseTarget')"
        clearable
        @update:value="targets.select"
      />
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
