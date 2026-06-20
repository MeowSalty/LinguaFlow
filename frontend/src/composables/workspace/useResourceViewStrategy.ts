import { computed, type Component } from 'vue'

import DirectoryView from '@/components/workspace/DirectoryView.vue'
import EpubDirectoryView from '@/components/workspace/EpubDirectoryView.vue'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'

import type {
  ResourceViewStrategy,
  ToolbarMeta,
  ViewStrategyName,
} from '@/types/resourceViewStrategy'

/** 策略注册表 - 便于未来扩展 */
const viewComponents: Record<ViewStrategyName, Component> = {
  directory: DirectoryView,
  'epub-directory': EpubDirectoryView,
}

/**
 * 资源视图策略 composable
 * 根据当前状态自动解析应该使用的视图组件和工具栏配置
 */
export function useResourceViewStrategy() {
  const workspace = useProjectWorkspaceStore()

  /** 当前策略名称（响应式） */
  const currentStrategyName = computed<ViewStrategyName>(() =>
    workspace.isInEpubDirectory ? 'epub-directory' : 'directory',
  )

  /** 当前策略的工具栏元数据 */
  const toolbarMeta = computed<ToolbarMeta>(() => {
    const isInEpub = workspace.isInEpubDirectory

    return {
      showBackButton: !!workspace.currentPath || isInEpub,
      showDivider: !!workspace.currentPath || isInEpub,
      showRefreshButton: !isInEpub,
      showUploadButton: !isInEpub,
      epubDirectoryActive: isInEpub,
    }
  })

  /** 当前活跃的视图组件（用于 component :is） */
  const activeViewComponent = computed(() => viewComponents[currentStrategyName.value])

  /** 当前完整策略对象 */
  const currentStrategy = computed<ResourceViewStrategy>(() => ({
    name: currentStrategyName.value,
    toolbarMeta: toolbarMeta.value,
  }))

  return {
    currentStrategyName,
    currentStrategy,
    toolbarMeta,
    activeViewComponent,
  }
}
