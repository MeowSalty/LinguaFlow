<script setup lang="ts">
import { computed, useId } from 'vue'
import { NInput, NSelect, useThemeVars } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import type { LimitedQuotaDraft } from '@/utils/storage-quota'
import {
  parseStorageCapacity,
  storageCapacityUnits,
  storageCapacityValue,
  type StorageCapacityUnit,
} from './capacity'

const props = defineProps<{ value: LimitedQuotaDraft; disabled?: boolean; label?: string }>()
const emit = defineEmits<{ 'update:value': [value: LimitedQuotaDraft] }>()
const { t } = useI18n()
const themeVars = useThemeVars()
const id = useId()
const bytes = computed(() => parseStorageCapacity(props.value.input, props.value.unit))
const invalid = computed(() => bytes.value === null)
const options = storageCapacityUnits.map((value) => ({ label: value, value }))

function update(input: string) {
  if (!props.disabled) emit('update:value', { ...props.value, input })
}
function changeUnit(unit: StorageCapacityUnit) {
  if (props.disabled) return
  const original = bytes.value
  emit('update:value', {
    mode: 'limited',
    unit,
    input: original === null ? props.value.input : storageCapacityValue(original, unit),
  })
}
</script>

<template>
  <div class="w-full min-w-0">
    <div class="flex min-w-0 gap-2">
      <NInput
        :value="value.input"
        :disabled="disabled"
        :status="invalid ? 'error' : undefined"
        :placeholder="t('storageCapacity.inputPlaceholder')"
        :input-props="{
          id,
          inputmode: 'decimal',
          'aria-label': label || t('storageCapacity.quota'),
          'aria-invalid': invalid,
          'aria-describedby': invalid ? `${id}-error` : `${id}-exact`,
        }"
        @update:value="update"
      />
      <NSelect
        class="!w-24 shrink-0"
        :value="value.unit"
        :disabled="disabled"
        :options="options"
        :aria-label="t('storageCapacity.unit')"
        @update:value="changeUnit"
      />
    </div>
    <p
      v-if="invalid"
      :id="`${id}-error`"
      class="mt-1.5 text-xs break-words"
      :style="{ color: themeVars.errorColor }"
      role="alert"
    >
      {{ t('storageCapacity.invalid') }}
    </p>
    <p
      v-else
      :id="`${id}-exact`"
      class="mt-1.5 text-xs text-lf-text-muted tabular-nums break-words"
    >
      {{ t('storageCapacity.exactBytes', { value: bytes!.toLocaleString() }) }}
    </p>
  </div>
</template>
