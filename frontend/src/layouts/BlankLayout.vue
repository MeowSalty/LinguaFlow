<script setup lang="ts">
defineProps<{
  title?: string
  subtitle?: string
}>()
</script>

<template>
  <div class="lf-auth-shell flex min-h-screen w-full items-center justify-center px-4 py-10">
    <div class="w-full max-w-105">
      <div class="mb-8 text-center">
        <RouterLink
          to="/"
          class="inline-flex items-center gap-2.5 text-2xl font-semibold tracking-tight text-lf-text-strong no-underline"
        >
          <span
            class="flex h-9 w-9 items-center justify-center rounded-xl bg-brand-500 text-sm font-bold text-white shadow-lg shadow-brand-500/30"
          >
            L
          </span>
          LinguaFlow
        </RouterLink>
        <h1 v-if="title" class="mt-6 text-2xl font-semibold tracking-tight text-lf-text-strong">
          {{ title }}
        </h1>
        <p v-if="subtitle" class="mt-2 text-sm leading-6 text-lf-text-muted">
          {{ subtitle }}
        </p>
      </div>

      <slot />
    </div>
  </div>
</template>
