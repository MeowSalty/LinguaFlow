<script setup lang="ts">
defineProps<{
  title?: string
  subtitle?: string
}>()
</script>

<template>
  <div
    class="min-h-screen w-full bg-gradient-to-br from-slate-50 via-white to-emerald-50/40 flex items-center justify-center px-4 py-10"
  >
    <div class="w-full max-w-[420px]">
      <div class="mb-8 text-center">
        <RouterLink
          to="/"
          class="inline-flex items-center gap-2 text-2xl font-bold tracking-tight text-brand-500"
        >
          LinguaFlow
        </RouterLink>
        <h1 v-if="title" class="mt-6 text-2xl font-semibold tracking-tight text-slate-900">
          {{ title }}
        </h1>
        <p v-if="subtitle" class="mt-2 text-sm text-slate-500">
          {{ subtitle }}
        </p>
      </div>

      <slot />
    </div>
  </div>
</template>
