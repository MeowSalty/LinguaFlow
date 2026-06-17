<script setup lang="ts">
import { NAlert, NButton, NModal } from 'naive-ui'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

defineProps<{
  show: boolean
  resourceName: string
  loading: boolean
}>()

const emit = defineEmits<{
  'update:show': [value: boolean]
  replace: []
  incremental: []
}>()
</script>

<template>
  <NModal
    :show="show"
    preset="card"
    :title="t('workspace.conflict.title')"
    :style="{ width: '440px' }"
    :bordered="false"
    :mask-closable="false"
    @update:show="(value: boolean) => emit('update:show', value)"
  >
    <div class="space-y-3">
      <NAlert type="warning" :bordered="false">
        {{ t('workspace.conflict.description', { name: resourceName }) }}
      </NAlert>
      <p class="text-sm text-lf-text-muted">
        {{ t('workspace.conflict.hint') }}
      </p>
    </div>
    <template #footer>
      <div class="flex justify-end gap-3">
        <NButton @click="emit('update:show', false)">
          {{ t('workspace.common.cancel') }}
        </NButton>
        <NButton :loading="loading" @click="emit('replace')">
          {{ t('workspace.conflict.fullReplace') }}
        </NButton>
        <NButton type="primary" @click="emit('incremental')">
          {{ t('workspace.conflict.incrementalUpdate') }}
        </NButton>
      </div>
    </template>
  </NModal>
</template>
