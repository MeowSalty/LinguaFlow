<script setup lang="ts">
import { NSpin } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import SegmentGroupItem from '@/components/workspace/SegmentGroupItem.vue'
import type { ResourceSegmentGroup } from '@/stores/projectWorkspace'

defineProps<{
  /** 是否正在加载章节 */
  loading: boolean
  /** 章节列表 */
  chapters: ResourceSegmentGroup[]
  /** 已选中的章节 group_key 集合 */
  selectedGroupKeys: Set<string>
}>()

const emit = defineEmits<{
  /** 点击章节 → 打开章节段落编辑 */
  click: [groupKey: string]
  /** 切换章节选中状态 */
  toggleSelect: [groupKey: string]
}>()

const { t } = useI18n()
</script>

<template>
  <!-- 加载状态 -->
  <div v-if="loading" class="flex justify-center py-8">
    <NSpin :size="24" />
  </div>

  <!-- 空章节 -->
  <div v-else-if="chapters.length === 0" class="py-12 text-center text-sm text-lf-text-muted">
    {{ t('workspace.epub.noChapters') }}
  </div>

  <!-- 章节列表 -->
  <div v-else class="space-y-1">
    <SegmentGroupItem
      v-for="chapter in chapters"
      :key="chapter.group_key"
      :group="chapter"
      :selected="selectedGroupKeys.has(chapter.group_key)"
      @click="emit('click', $event)"
      @toggle-select="emit('toggleSelect', $event)"
    />
  </div>
</template>
