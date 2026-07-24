import { defineConfig } from 'vitepress'

const base = process.env.DOCS_BASE || '/'

export default defineConfig({
  title: 'LinguaFlow',
  description: 'AI 驱动的多语言翻译工作台',
  base,

  sitemap: {
    hostname: 'https://meowsalty.github.io/LinguaFlow',
  },

  head: [
    ['link', { rel: 'icon', type: 'image/svg+xml', href: `${base}logo.svg` }],
  ],

  locales: {
    zh: {
      label: '中文',
      lang: 'zh-CN',
      themeConfig: {
        nav: [
          { text: '首页', link: '/zh/' },
          {
            text: '指南',
            link: '/zh/guide/getting-started',
            activeMatch: '/zh/guide/',
          },
          { text: 'API', link: '/zh/api/' },
        ],
        sidebar: {
          '/zh/guide/': [
            {
              text: '开始使用',
              items: [
                { text: '快速开始 · Web', link: '/zh/guide/getting-started' },
                { text: '快速开始 · CLI', link: '/zh/guide/cli-quickstart' },
                { text: '核心概念', link: '/zh/guide/concepts' },
                { text: '安装部署', link: '/zh/guide/installation' },
                { text: '使用模式', link: '/zh/guide/modes' },
              ],
            },
            {
              text: '日常使用',
              items: [
                { text: '项目管理', link: '/zh/guide/projects' },
                { text: '术语表管理', link: '/zh/guide/glossary' },
                { text: '翻译审校', link: '/zh/guide/review' },
                { text: '格式支持', link: '/zh/guide/formats' },
              ],
            },
            {
              text: '配置与进阶',
              items: [
                { text: '翻译配置', link: '/zh/guide/translation-config' },
                { text: '高级功能', link: '/zh/guide/advanced' },
                { text: '配置参考', link: '/zh/guide/configuration' },
                { text: 'CLI 参考', link: '/zh/guide/cli' },
                { text: '常见问题', link: '/zh/guide/faq' },
              ],
            },
          ],
          '/zh/api/': [
            {
              text: 'API 参考',
              items: [
                { text: '概述', link: '/zh/api/' },
              ],
            },
          ],
        },
        footer: {
          message: '基于 GNU AGPL v3 许可证发布',
          copyright: '© 2026-present LinguaFlow',
        },
        outline: {
          label: '本页目录',
        },
        docFooter: {
          prev: '上一页',
          next: '下一页',
        },
        returnToTopLabel: '回到顶部',
        sidebarMenuLabel: '菜单',
        darkModeSwitchLabel: '主题',
        lightModeSwitchTitle: '切换到浅色',
        darkModeSwitchTitle: '切换到深色',
      },
    },
    en: {
      label: 'English',
      lang: 'en-US',
      themeConfig: {
        nav: [
          { text: 'Home', link: '/en/' },
          { text: 'Guide', link: '/en/guide/getting-started' },
          { text: 'API', link: '/en/api/' },
        ],
        sidebar: {
          // English pages are incomplete; only list existing routes to avoid 404s.
          // Full docs are available in Chinese under /zh/.
          '/en/guide/': [
            {
              text: 'Getting Started',
              items: [
                { text: 'Quick Start', link: '/en/guide/getting-started' },
                { text: 'Configuration', link: '/en/guide/configuration' },
              ],
            },
          ],
          '/en/api/': [
            {
              text: 'API Reference',
              items: [
                { text: 'Overview', link: '/en/api/' },
              ],
            },
          ],
        },
        footer: {
          message: 'Released under the GNU AGPL v3 License',
          copyright: '© 2026-present LinguaFlow',
        },
      },
    },
  },

  themeConfig: {
    logo: '/logo.svg',

    socialLinks: [
      { icon: 'github', link: 'https://github.com/MeowSalty/LinguaFlow' },
    ],

    search: {
      provider: 'local',
      options: {
        locales: {
          zh: {
            translations: {
              button: {
                buttonText: '搜索',
                buttonAriaLabel: '搜索文档',
              },
              modal: {
                noResultsText: '没有相关结果',
                resetButtonTitle: '清除查询',
                footer: {
                  selectText: '选择',
                  navigateText: '切换',
                  closeText: '关闭',
                },
              },
            },
          },
        },
      },
    },

    editLink: {
      pattern: 'https://github.com/MeowSalty/LinguaFlow/edit/main/docs/:path',
      text: '在 GitHub 上编辑此页',
    },
  },
})