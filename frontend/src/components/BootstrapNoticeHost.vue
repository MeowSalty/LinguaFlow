<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useMessage } from 'naive-ui'

import { consumeBootstrapNotices } from '@/bootstrap'

const message = useMessage()
const { t } = useI18n()

onMounted(() => {
  for (const notice of consumeBootstrapNotices()) {
    if (notice === 'localUserFailed') {
      message.warning(t('appBootstrap.errors.localUserFailed'))
    } else if (notice === 'modeUnreachable') {
      message.warning(t('appBootstrap.errors.modeUnreachable'))
    }
  }
})
</script>

<template />
