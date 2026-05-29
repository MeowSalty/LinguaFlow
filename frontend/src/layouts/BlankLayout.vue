<script setup lang="ts">
defineProps<{
  title?: string
  subtitle?: string
}>()
</script>

<template>
  <div class="flex min-h-screen w-full items-center justify-center bg-lf-bg px-4 py-10">
    <div class="w-full max-w-105">
      <div class="mb-8 text-center">
        <RouterLink
          to="/"
          class="inline-flex items-center gap-2 text-2xl font-bold tracking-tight text-brand-500"
        >
          LinguaFlow
        </RouterLink>
        <h1 v-if="title" class="mt-6 text-2xl font-semibold tracking-tight text-lf-text-strong">
          {{ title }}
        </h1>
        <p v-if="subtitle" class="mt-2 text-sm text-lf-text-muted">
          {{ subtitle }}
        </p>
      </div>

      <slot />
    </div>
  </div>
</template>
