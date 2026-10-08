<script setup lang="ts">
import { shallowRef, useId, watch } from 'vue'
import { NRadio, NRadioGroup } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import type { LimitedQuotaDraft, QuotaDraft } from '@/utils/storage-quota'
import StorageCapacityInput from './StorageCapacityInput.vue'

const props = defineProps<{ value: QuotaDraft; disabled?: boolean; label?: string }>()
const emit = defineEmits<{ 'update:value': [value: QuotaDraft] }>()
const { t } = useI18n()
const id = useId()
const finite = shallowRef<LimitedQuotaDraft>({ mode: 'limited', input: '', unit: 'GiB' })
watch(
  () => props.value,
  (value) => {
    if (value.mode === 'limited') finite.value = { ...value }
    else if (value.mode === 'unselected') finite.value = { mode: 'limited', input: '', unit: 'GiB' }
  },
  { immediate: true },
)
function choose(mode: string | number | boolean) {
  if (props.disabled) return
  if (mode === 'limited') emit('update:value', { ...finite.value })
  else if (mode === 'unlimited') emit('update:value', { mode })
}
function update(value: LimitedQuotaDraft) {
  if (props.disabled) return
  finite.value = { ...value }
  emit('update:value', value)
}
</script>

<template>
  <div class="w-full min-w-0 space-y-3">
    <NRadioGroup
      :value="value.mode === 'unselected' ? null : value.mode"
      :disabled="disabled"
      :name="id"
      :aria-label="label || t('storageCapacity.quota')"
      :aria-describedby="value.mode === 'unselected' ? `${id}-required` : undefined"
      @update:value="choose"
    >
      <div class="flex flex-wrap gap-x-5 gap-y-2">
        <NRadio value="unlimited">{{ t('storageCapacity.unlimited') }}</NRadio>
        <NRadio value="limited">{{ t('storageCapacity.setLimit') }}</NRadio>
      </div>
    </NRadioGroup>
    <StorageCapacityInput
      v-if="value.mode === 'limited'"
      :value="value"
      :disabled="disabled"
      :label="label"
      @update:value="update"
    />
    <p v-if="value.mode === 'unselected'" :id="`${id}-required`" class="text-xs text-lf-text-muted">
      {{ t('storageCapacity.selectMode') }}
    </p>
  </div>
</template>
