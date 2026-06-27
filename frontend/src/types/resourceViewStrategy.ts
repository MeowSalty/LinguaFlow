/**
 * 工具栏元数据 - 由当前活跃的视图策略提供
 * ResourceExplorer 根据此元数据渲染工具栏
 */
export interface ToolbarMeta {
  /** 是否显示返回上级按钮 */
  showBackButton: boolean
  /** 是否显示分隔线 */
  showDivider: boolean
  /** 是否显示刷新按钮 */
  showRefreshButton: boolean
  /** 是否显示上传按钮 */
  showUploadButton: boolean
  /** 面包屑末尾是否为虚拟目录（禁用点击） */
  epubDirectoryActive: boolean
}

/**
 * 视图策略标识
 */
export type ViewStrategyName = 'directory' | 'epub-directory'

/**
 * 视图策略描述符
 * 每种资源展示模式实现一个策略
 */
export interface ResourceViewStrategy {
  /** 策略唯一名称 */
  name: ViewStrategyName
  /** 工具栏元数据 */
  toolbarMeta: ToolbarMeta
}
