<script setup lang="ts">
import { NBadge, NButton, NIcon } from 'naive-ui'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

defineProps<{
  count: number
  canTranslate: boolean
}>()

defineEmits<{
  translate: []
  clear: []
}>()
</script>

<template>
  <Transition name="slide-up">
    <div
      v-if="count > 0"
      class="pointer-events-none fixed inset-x-0 bottom-6 z-50 flex justify-center px-4"
    >
      <div
        class="pointer-events-auto flex items-center gap-4 rounded-2xl border border-lf-border-soft bg-lf-surface px-5 py-3 shadow-lg shadow-lf-shadow-strong backdrop-blur-sm"
      >
        <!-- 选中数量 -->
        <NBadge :value="count" :max="99" type="success" />

        <!-- 分隔线 -->
        <span class="h-5 w-px bg-lf-border-soft" />

        <!-- 操作按钮 -->
        <div class="flex items-center gap-2">
          <NButton
            type="primary"
            size="small"
            :disabled="!canTranslate"
            @click="$emit('translate')"
          >
            <template #icon>
              <NIcon><IconCarbonMagicWand /></NIcon>
            </template>
            {{ t('workspace.selection.translate') }}
          </NButton>
          <NButton quaternary size="small" @click="$emit('clear')">
            {{ t('workspace.selection.clear') }}
          </NButton>
        </div>
      </div>
    </div>
  </Transition>
</template>

<style scoped>
.slide-up-enter-active,
.slide-up-leave-active {
  transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1);
}
.slide-up-enter-from,
.slide-up-leave-to {
  opacity: 0;
  transform: translateY(16px);
}
.slide-up-enter-to,
.slide-up-leave-from {
  opacity: 1;
  transform: translateY(0);
}
</style>
