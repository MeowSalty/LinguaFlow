import type { Component } from 'vue'

import IconCarbonDashboard from '~icons/carbon/dashboard'
import IconCarbonFolder from '~icons/carbon/folder'
import IconCarbonServerProxy from '~icons/carbon/server-proxy'
import IconCarbonChartBar from '~icons/carbon/chart-bar'
import IconCarbonPromptTemplate from '~icons/carbon/prompt-template'
import IconCarbonTextMining from '~icons/carbon/text-mining'
import IconCarbonClean from '~icons/carbon/clean'
import IconCarbonFlow from '~icons/carbon/flow'
import IconCarbonPlan from '~icons/carbon/plan'
import IconCarbonTextVerticalAlignment from '~icons/carbon/text-vertical-alignment'
import IconCarbonSecurity from '~icons/carbon/security'

export interface AppNavItem {
  /** 目标路由路径；'/' 仅在精确匹配时视为激活 */
  path: string
  labelKey: string
  icon: Component
}

export interface AppNavSection {
  /** 分组标签 i18n key；null 表示无标签的独立分区（与前一分区之间渲染分隔线） */
  labelKey: string | null
  items: AppNavItem[]
  /** 仅管理员可见 */
  adminOnly?: boolean
}

/** 全应用导航的单一数据源：桌面侧边栏与移动端抽屉共用同一份渲染 */
export const APP_NAV_SECTIONS: AppNavSection[] = [
  {
    labelKey: null,
    items: [
      { path: '/', labelKey: 'nav.dashboard', icon: IconCarbonDashboard },
      { path: '/projects', labelKey: 'nav.projects', icon: IconCarbonFolder },
      { path: '/backends', labelKey: 'nav.backends', icon: IconCarbonServerProxy },
      { path: '/stats', labelKey: 'nav.stats', icon: IconCarbonChartBar },
    ],
  },
  {
    labelKey: 'nav.executionConfig',
    items: [
      {
        path: '/prompt-templates',
        labelKey: 'nav.promptTemplates',
        icon: IconCarbonPromptTemplate,
      },
      {
        path: '/bootstrap-prompt-templates',
        labelKey: 'nav.bootstrapPromptTemplates',
        icon: IconCarbonTextMining,
      },
      {
        path: '/prune-prompt-templates',
        labelKey: 'nav.prunePromptTemplates',
        icon: IconCarbonClean,
      },
      { path: '/execution-profiles', labelKey: 'nav.executionProfiles', icon: IconCarbonFlow },
      {
        path: '/execution-plan-templates',
        labelKey: 'nav.executionPlanTemplates',
        icon: IconCarbonPlan,
      },
    ],
  },
  {
    labelKey: 'nav.tools',
    items: [
      {
        path: '/tools/epub-rotate',
        labelKey: 'nav.epubRotate',
        icon: IconCarbonTextVerticalAlignment,
      },
    ],
  },
  {
    labelKey: null,
    adminOnly: true,
    items: [{ path: '/admin', labelKey: 'nav.admin', icon: IconCarbonSecurity }],
  },
]
