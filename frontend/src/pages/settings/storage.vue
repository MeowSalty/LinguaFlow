<script setup lang="ts">
import StorageManager from '@/components/storage/StorageManager.vue'
</script>
<template>
  <StorageManager :scope="{ kind: 'user' }" />
</template>
