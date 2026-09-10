<script setup lang="ts">
withDefaults(
  defineProps<{
    size?: 'sm' | 'md' | 'lg'
    wordmark?: boolean
  }>(),
  { size: 'md', wordmark: true },
)
</script>

<template>
  <span class="inline-flex items-center gap-2.5">
    <span
      class="lf-grad-bg flex shrink-0 items-center justify-center font-bold text-white"
      :class="{
        'h-7 w-7 rounded-lg text-xs': size === 'sm',
        'h-9 w-9 rounded-xl text-sm': size === 'md',
        'h-11 w-11 rounded-xl text-base': size === 'lg',
      }"
    >
      L
    </span>
    <span
      v-if="wordmark"
      class="text-[15.5px] font-bold tracking-tight whitespace-nowrap text-lf-text-strong"
    >
      LinguaFlow
    </span>
  </span>
</template>
