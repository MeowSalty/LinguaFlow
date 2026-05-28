<script setup lang="ts">
import AppLayout from '@/layouts/AppLayout.vue'

const route = useRoute()
const isBlank = computed(() => route.meta.layout === 'blank')
</script>

<template>
  <NConfigProvider
    :theme-overrides="{
      common: {
        primaryColor: '#18a058',
        primaryColorHover: '#36ad6a',
        primaryColorPressed: '#0c7a43',
      },
    }"
  >
    <NMessageProvider>
      <RouterView v-slot="{ Component }">
        <component :is="Component" v-if="isBlank" />
        <AppLayout v-else>
          <component :is="Component" />
        </AppLayout>
      </RouterView>
    </NMessageProvider>
  </NConfigProvider>
</template>
