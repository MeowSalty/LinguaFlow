<script setup lang="ts">
/**
 * 抽屉头部：标题 + 可选副标题两行文本，供 NDrawerContent 的 #header 插槽使用。
 */
defineProps<{
  title: string
  subtitle?: string
}>()
</script>

<template>
  <div>
    <div class="text-lg font-semibold">{{ title }}</div>
    <div v-if="subtitle" class="mt-1 text-xs text-lf-text-muted">{{ subtitle }}</div>
  </div>
</template>
