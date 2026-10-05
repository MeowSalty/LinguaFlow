<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NAlert, NButton, NSkeleton, NTag } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import type { ApiSchemas } from '@/api/client-core'
import { useStorageStore } from '@/stores/storage'
import StorageCapacity from './StorageCapacity.vue'

const props = defineProps<{ connectionId: number; compact?: boolean }>()
const emit = defineEmits<{
  create: []
  inspect: [spaceId: number, event: MouseEvent]
  change: [space: ApiSchemas['StorageSpace']]
}>()
const { t } = useI18n()
const store = useStorageStore()
const expanded = ref(false)
const state = computed(() => store.spaces[props.connectionId])
const rows = computed(() => {
  const spaces = state.value?.items ?? []
  return props.compact && !expanded.value ? spaces.slice(0, 3) : spaces
})
watch(
  () => props.connectionId,
  () => {
    expanded.value = false
  },
)
</script>

<template>
  <div class="min-w-0" :aria-busy="state?.loading || !state">
    <NAlert v-if="state?.error" type="warning" class="mb-3" :bordered="false">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <span>{{ state.error }}</span>
        <NButton text :loading="state.loading" @click="store.loadSpaces(connectionId, true)">
          {{ t('storage.retry') }}
        </NButton>
      </div>
    </NAlert>
    <p v-if="state?.stale" class="mb-3 text-xs text-lf-text-muted" role="status">
      {{ t('storage.stale') }}
    </p>
    <div v-if="!state || (state.loading && !state.loaded)" class="space-y-3 py-3">
      <NSkeleton text width="38%" />
      <NSkeleton text width="85%" />
    </div>
    <div
      v-else-if="state.loaded && !state.items.length"
      class="flex flex-wrap items-center justify-between gap-3 py-4"
    >
      <p class="text-sm text-lf-text-muted">{{ t('storageUi.noSpaces') }}</p>
      <NButton
        v-if="compact && store.connectionAllowed(connectionId, 'create_space')"
        size="small"
        secondary
        @click="emit('create')"
      >
        {{ t('storage.createSpace') }}
      </NButton>
    </div>
    <div v-else class="divide-y divide-lf-border-soft">
      <article
        v-for="space in rows"
        :key="space.id"
        :data-storage-space-id="space.id"
        class="min-w-0 py-4 first:pt-1 last:pb-1"
      >
        <div
          class="grid min-w-0 gap-3"
          :class="compact ? 'sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] sm:items-center' : ''"
        >
          <div class="min-w-0">
            <button
              v-if="compact"
              type="button"
              class="max-w-full cursor-pointer rounded text-left text-sm font-medium break-words text-lf-text-strong underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-brand-500 [overflow-wrap:anywhere]"
              :aria-label="t('storageUi.spaceDetails', { name: space.name })"
              @click="emit('inspect', space.id, $event)"
            >
              {{ space.name }}
            </button>
            <h3
              v-else
              class="text-sm font-semibold break-words text-lf-text-strong [overflow-wrap:anywhere]"
            >
              {{ space.name }}
            </h3>
            <div class="mt-2 flex flex-wrap items-center gap-2">
              <NTag size="small" :bordered="false">{{
                ['active', 'read_only', 'disabled'].includes(space.status)
                  ? t(`storage.states.${space.status}`)
                  : t('storage.unknown')
              }}</NTag>
              <span class="text-xs text-lf-text-muted">{{
                t(space.verified ? 'storage.verified' : 'storage.unverified')
              }}</span>
            </div>
            <p v-if="!compact && space.bucket" class="mt-2 text-xs break-all text-lf-text-muted">
              {{ space.bucket }} / {{ space.prefix }}
            </p>
          </div>
          <StorageCapacity :space="space" :compact="compact" />
        </div>
        <div v-if="!compact" class="mt-3 space-y-2">
          <NButton
            size="small"
            :disabled="
              !store.spaceAllowed(connectionId, space.id) ||
              state?.stale ||
              !['active', 'read_only', 'disabled'].includes(space.status)
            "
            @click="emit('change', space)"
          >
            {{ t(space.status === 'active' ? 'storage.makeReadOnly' : 'storage.makeActive') }}
          </NButton>
          <p
            v-if="store.spaceReason(connectionId, space.id)"
            class="text-xs leading-5 text-lf-text-muted"
          >
            {{ store.spaceReason(connectionId, space.id) }}
          </p>
        </div>
      </article>
    </div>
    <NButton
      v-if="compact && (state?.items.length ?? 0) > 3"
      text
      class="mt-4"
      @click="expanded = !expanded"
    >
      {{
        expanded
          ? t('storageUi.collapseSpaces')
          : t('storageUi.showSpaces', { count: (state?.items.length ?? 0) - 3 })
      }}
    </NButton>
  </div>
</template>
