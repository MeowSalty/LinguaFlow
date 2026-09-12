import { onBeforeUnmount, onMounted, ref, watch, type Ref } from 'vue'

/**
 * 编辑视图「席位常驻」双面板的响应式状态机。
 *
 * 布局约定：宽屏下左右席位常驻预留（面板开合不影响文档流宽度），
 * 面板以 Teleport 单实例在席位与抽屉之间切换，不重挂载、不丢焦点。
 *
 * 差异化桥接规则（对应断点跨越时的行为）：
 * - 章节目录（断点 lg=1024）：宽屏默认显示；变窄静默收起（用户手动开抽屉），
 *   变宽自动恢复停靠。跨越时只处理章节自己的抽屉，不触碰搜索抽屉。
 * - 搜索定位（断点 xl=1280）：默认关闭，用户打开才算「使用中」；变窄时若
 *   正在使用则自动转为抽屉（不打断输入焦点与结果），变宽时抽屉自动收回恢复停靠。
 *   跳转后仅隐藏抽屉（hideSearchDrawer）保持「使用中」；手动收起（✕/ESC/遮罩）
 *   则置 searchOpen=false。两种收起都只是意图表达，面板卸载与否由 SegmentPanel
 *   的挂载门（searchPanelMounted）控制——常驻挂载仅转不可见，会话状态不丢。
 *
 * 断点检测以响应式 viewportWidth 为单一事实源：resize / visualViewport resize /
 * setInterval 轮询三条路径都只负责更新宽度，状态迁移统一由 watch 驱动（部分
 * 嵌入视口缩放场景下 resize 事件与 rAF 都被暂停，轮询兜底保证状态最终一致）。
 */
export interface ResponsiveDockState {
  /** 章节目录是否展开（用户意图，与席位是否可见无关） */
  chaptersOpen: Ref<boolean>
  /** 搜索定位是否展开（用户意图；打开即视为「使用中」） */
  searchOpen: Ref<boolean>
  /** 章节席位是否停靠（视口 ≥ lg） */
  chaptersDocked: Ref<boolean>
  /** 搜索席位是否停靠（视口 ≥ xl） */
  searchDocked: Ref<boolean>
  /** 章节抽屉可见（席位不可用时用户手动打开） */
  chaptersDrawerVisible: Ref<boolean>
  /** 搜索抽屉可见（席位不可用时自动/手动打开） */
  searchDrawerVisible: Ref<boolean>
  /** 章节面板当前应以抽屉呈现（决定 Teleport 与遮罩） */
  chaptersInDrawer: Ref<boolean>
  /** 搜索面板当前应以抽屉呈现 */
  searchInDrawer: Ref<boolean>
  toggleChapters: () => void
  toggleSearch: () => void
  openChaptersDrawer: () => void
  openSearchDrawer: () => void
  closeChaptersDrawer: () => void
  closeSearchDrawer: () => void
  /** 跳转定位后隐藏搜索抽屉但保留搜索会话（searchOpen 不变；面板由挂载门保持挂载，重开时状态原样恢复） */
  hideSearchDrawer: () => void
}

// 停靠断点按几何推算：中央内容列恢复全站限宽 max-w-275(1100px) 居中，
// 面板吸附在内容列两侧的空白区——两侧空白各需容纳面板宽 + 间隙才停靠。
// 章节 232px+12 间隙：1100 + 2×244 = 1588；搜索 344px+12：1100 + 2×356 = 1812。
// 注意：这是「内容区宽度」（不含全局侧边栏），不是视口宽度。
const LG_BREAKPOINT = 1588
const XL_BREAKPOINT = 1812

export const useResponsiveDock = (): ResponsiveDockState => {
  const chaptersOpen = ref(true)
  const searchOpen = ref(false)
  const chaptersDocked = ref(true)
  const searchDocked = ref(true)
  const chaptersDrawerVisible = ref(false)
  const searchDrawerVisible = ref(false)
  const chaptersInDrawer = ref(false)
  const searchInDrawer = ref(false)

  // ── 单一事实源：内容区宽度（编辑视图挂载的 main 区，不含全局侧边栏）。
  //    多路径写入（见下），watch 统一驱动状态迁移 ──
  const contentWidth = ref(XL_BREAKPOINT)

  const syncInDrawer = (): void => {
    chaptersInDrawer.value =
      !chaptersDocked.value && chaptersDrawerVisible.value && chaptersOpen.value
    searchInDrawer.value = !searchDocked.value && searchDrawerVisible.value && searchOpen.value
  }

  const toggleChapters = (): void => {
    chaptersOpen.value = !chaptersOpen.value
    if (!chaptersOpen.value) chaptersDrawerVisible.value = false
    syncInDrawer()
  }

  const toggleSearch = (): void => {
    searchOpen.value = !searchOpen.value
    if (!searchOpen.value) searchDrawerVisible.value = false
    syncInDrawer()
  }

  const openChaptersDrawer = (): void => {
    chaptersOpen.value = true
    chaptersDrawerVisible.value = true
    syncInDrawer()
  }

  const openSearchDrawer = (): void => {
    searchOpen.value = true
    searchDrawerVisible.value = true
    syncInDrawer()
  }

  const closeChaptersDrawer = (): void => {
    // 手动关抽屉视为完全收起；变宽时不再自动恢复
    chaptersOpen.value = false
    chaptersDrawerVisible.value = false
    syncInDrawer()
  }

  const closeSearchDrawer = (): void => {
    searchOpen.value = false
    searchDrawerVisible.value = false
    syncInDrawer()
  }

  /** 跳转定位后隐藏抽屉但保持「使用中」：面板实例继续挂载（仅不可见），
      输入、滚动位置与选中态不丢，重开抽屉原样恢复；变宽时照常恢复停靠 */
  const hideSearchDrawer = (): void => {
    searchDrawerVisible.value = false
    syncInDrawer()
  }

  const handleChaptersChange = (matches: boolean): void => {
    const wasDocked = chaptersDocked.value
    if (wasDocked === matches) return
    chaptersDocked.value = matches
    // 宽→窄：静默收起（不自动弹抽屉，用户可手动开）；窄→宽：开着的抽屉收回恢复停靠。
    // 跨越时只处理章节自己的抽屉，不触碰搜索抽屉。
    chaptersDrawerVisible.value = false
    syncInDrawer()
  }

  const handleSearchChange = (matches: boolean): void => {
    const wasDocked = searchDocked.value
    if (wasDocked === matches) return
    searchDocked.value = matches
    if (!matches) {
      // 宽 → 窄：使用中则自动转抽屉，避免打断输入与结果
      if (searchOpen.value) {
        searchDrawerVisible.value = true
      }
    } else {
      // 窄 → 宽：搜索抽屉收回，恢复停靠
      searchDrawerVisible.value = false
    }
    syncInDrawer()
  }

  // 内容区宽度变化 → 统一驱动断点迁移（幂等：dock 状态未变时 handler 直接返回）
  watch(contentWidth, (width) => {
    handleChaptersChange(width >= LG_BREAKPOINT)
    handleSearchChange(width >= XL_BREAKPOINT)
  })

  // ── 多路径宽度采集：以编辑视图根元素（main 区直下）的 clientWidth 为准，
  //    排除全局侧边栏；真实浏览器里 resize/change 足够，嵌入视口缩放
  //    （如应用内浏览器）resize 与 rAF 都可能被暂停，setInterval 轮询兜底
  //    最终一致。编辑视图挂载期之外组件已卸载，轮询自动停止。 ──
  let pollTimer: ReturnType<typeof setInterval> | null = null

  const collectWidth = (): void => {
    const host = document.querySelector('main')
    const width = host ? Math.round(host.getBoundingClientRect().width) : window.innerWidth
    if (width !== contentWidth.value) {
      contentWidth.value = width
    }
  }

  onMounted(() => {
    collectWidth()
    window.addEventListener('resize', collectWidth)
    window.visualViewport?.addEventListener('resize', collectWidth)
    pollTimer = setInterval(collectWidth, 200)
  })

  onBeforeUnmount(() => {
    window.removeEventListener('resize', collectWidth)
    window.visualViewport?.removeEventListener('resize', collectWidth)
    if (pollTimer) clearInterval(pollTimer)
  })

  syncInDrawer()

  return {
    chaptersOpen,
    searchOpen,
    chaptersDocked,
    searchDocked,
    chaptersDrawerVisible,
    searchDrawerVisible,
    chaptersInDrawer,
    searchInDrawer,
    toggleChapters,
    toggleSearch,
    openChaptersDrawer,
    openSearchDrawer,
    closeChaptersDrawer,
    closeSearchDrawer,
    hideSearchDrawer,
  }
}
