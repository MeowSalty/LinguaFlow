<script setup lang="ts">
/**
 * 通用页头：标题 + 可选副标题 + 右侧操作区。
 */
defineProps<{
  title: string
  subtitle?: string
}>()
</script>

<template>
  <section class="lf-page-header">
    <div class="space-y-1.5">
      <h1 class="text-2xl font-semibold tracking-tight text-lf-text-strong">
        {{ title }}
      </h1>
      <p v-if="subtitle" class="max-w-2xl text-sm leading-6 text-lf-text-muted">
        {{ subtitle }}
      </p>
    </div>
    <div v-if="$slots.actions" class="flex flex-wrap items-center gap-3">
      <slot name="actions" />
    </div>
  </section>
</template>
