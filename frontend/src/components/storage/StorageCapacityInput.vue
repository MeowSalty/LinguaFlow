<script setup lang="ts">
import { computed, shallowRef, useId, watch } from 'vue'
import { useThemeVars } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import {
  parseStorageCapacity,
  storageCapacityUnit,
  storageCapacityUnits,
  storageCapacityValue,
  type StorageCapacityUnit,
} from './capacity'

const props = defineProps<{ value: number; disabled?: boolean; label?: string }>()
const emit = defineEmits<{ 'update:value': [value: number] }>()
const { t } = useI18n()
const themeVars = useThemeVars()
const id = useId()
const unit = shallowRef<StorageCapacityUnit>(storageCapacityUnit(props.value))
const draft = shallowRef(storageCapacityValue(props.value, unit.value))
const touched = shallowRef(false)
const bytes = computed(() => parseStorageCapacity(draft.value, unit.value))
const invalid = computed(() => touched.value && bytes.value === null)
const options = storageCapacityUnits.map((value) => ({ label: value, value }))
let emittedValue: number | undefined

watch(
  () => props.value,
  (value) => {
    if (value === emittedValue) {
      emittedValue = undefined
      return
    }
    draft.value = storageCapacityValue(value, unit.value)
    touched.value = false
  },
)

function update(value: string) {
  draft.value = value
  touched.value = true
  emittedValue = bytes.value ?? 0
  emit('update:value', emittedValue)
}
function changeUnit(value: StorageCapacityUnit) {
  const original = bytes.value
  unit.value = value
  if (original !== null) draft.value = storageCapacityValue(original, value)
  else update(draft.value)
}
</script>

<template>
  <div class="w-full min-w-0">
    <div class="flex min-w-0 gap-2">
      <n-input
        :value="draft"
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
      <n-select
        class="!w-24 shrink-0"
        :value="unit"
        :disabled="disabled"
        :options="options"
        :aria-label="t('storageCapacity.unit')"
        @update:value="changeUnit"
      />
    </div>
    <p
      v-if="invalid"
      :id="`${id}-error`"
      class="mt-1.5 text-xs"
      :style="{ color: themeVars.errorColor }"
      role="alert"
    >
      {{ t('storageCapacity.invalid') }}
    </p>
    <p v-else :id="`${id}-exact`" class="mt-1.5 text-xs text-lf-text-muted tabular-nums">
      {{
        bytes === null
          ? t('storageCapacity.inputHint')
          : t('storageCapacity.exactBytes', { value: bytes.toLocaleString() })
      }}
    </p>
  </div>
</template>
