<script setup lang="ts">
import { computed } from 'vue'
import { NSelect } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { useOrganizationsStore } from '@/stores/organizations'
import { useServiceStore } from '@/stores/service'
defineProps<{ value: number | null }>()
const emit = defineEmits<{ 'update:value': [value: number | null] }>()
const { t } = useI18n()
const store = useOrganizationsStore()
const service = useServiceStore()
const options = computed(() => [
  { label: t('team.personal'), value: 0 },
  ...store.items.map((org) => ({ label: org.display_name || org.name, value: org.id })),
])
</script>
<template>
  <NSelect
    v-if="!service.isLocal"
    class="min-w-48 lg:max-w-xs!"
    :aria-label="t('team.scope')"
    :value="value ?? 0"
    :options="options"
    :loading="store.loading"
    @update:value="(value) => emit('update:value', value === 0 ? null : value)"
  />
</template>
