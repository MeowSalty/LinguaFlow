<script lang="ts">
/** 计数分段筛选标签的类型定义：tabs 数据由调用方从 store 计数构建。 */
export interface ScopeFilterTab {
  name: string
  label: string
  count: number
}
</script>

<script setup lang="ts">
/**
 * 计数分段筛选标签：NTabs segment + 「标签 + 计数」渲染。
 * 实体管理页（执行策略、执行计划等）筛选栏共用，替代各自重复的 renderFilterTab 实现。
 */
import { NTab, NTabs } from 'naive-ui'
import { h } from 'vue'

defineProps<{
  tabs: ScopeFilterTab[]
  value: string
}>()

const emit = defineEmits<{
  'update:value': [value: string]
}>()

const renderFilterTab = (tab: ScopeFilterTab): ReturnType<typeof h> =>
  h('span', { class: 'inline-flex items-baseline gap-1.5' }, [
    tab.label,
    h(
      'span',
      { class: 'hidden text-xs font-normal text-lf-text-subtle sm:inline' },
      String(tab.count),
    ),
  ])
</script>

<template>
  <NTabs
    :value="value"
    type="segment"
    size="small"
    class="min-w-0"
    @update:value="(val: string | number) => emit('update:value', String(val))"
  >
    <NTab v-for="tab in tabs" :key="tab.name" :name="tab.name" :tab="() => renderFilterTab(tab)" />
  </NTabs>
</template>
