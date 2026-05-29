<script setup lang="ts">
import { useI18n } from 'vue-i18n'

const { t } = useI18n()
</script>

<template>
  <NCard :title="t('about.title')" :bordered="false" class="shadow-sm shadow-lf-shadow">
    <p class="text-sm text-lf-text">
      {{ t('about.description') }}
    </p>
    <p class="mt-3 text-sm text-lf-text-muted">
      {{ t('about.sourcePrefix') }}
      <code class="rounded bg-lf-code-bg px-1 py-0.5">src/pages/about.vue</code>
      {{ t('about.sourceSuffix') }}
      <code class="rounded bg-lf-code-bg px-1 py-0.5">/about</code>。
    </p>
  </NCard>
</template>
