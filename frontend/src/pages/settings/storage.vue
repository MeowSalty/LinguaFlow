<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import StorageManager from '@/components/storage/StorageManager.vue'
const { t } = useI18n()
</script>
<template>
  <div class="space-y-5">
    <p class="text-lf-text-muted">{{ t('storage.subtitle') }}</p>
    <StorageManager :scope="{ kind: 'user' }" />
  </div>
</template>
