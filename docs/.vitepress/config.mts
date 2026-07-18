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
          { text: '指南', link: '/zh/guide/getting-started' },
          { text: 'API', link: '/zh/api/' },
        ],
        sidebar: {
          '/zh/guide/': [
            {
              text: '入门',
              items: [
                { text: '快速开始', link: '/zh/guide/getting-started' },
                { text: '安装部署', link: '/zh/guide/installation' },
                { text: '使用模式', link: '/zh/guide/modes' },
                { text: '配置', link: '/zh/guide/configuration' },
              ],
            },
            {
              text: '使用指南',
              items: [
                { text: 'CLI 命令行', link: '/zh/guide/cli' },
                { text: '项目管理', link: '/zh/guide/projects' },
                { text: '翻译配置', link: '/zh/guide/translation-config' },
                { text: '术语表管理', link: '/zh/guide/glossary' },
                { text: '翻译审校', link: '/zh/guide/review' },
                { text: '格式支持', link: '/zh/guide/formats' },
              ],
            },
            {
              text: '进阶',
              items: [
                { text: '高级功能', link: '/zh/guide/advanced' },
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
          '/en/guide/': [
            {
              text: 'Getting Started',
              items: [
                { text: 'Quick Start', link: '/en/guide/getting-started' },
                { text: 'Installation', link: '/en/guide/installation' },
                { text: 'Modes', link: '/en/guide/modes' },
                { text: 'Configuration', link: '/en/guide/configuration' },
              ],
            },
            {
              text: 'User Guide',
              items: [
                { text: 'CLI', link: '/en/guide/cli' },
                { text: 'Project Management', link: '/en/guide/projects' },
                { text: 'Translation Config', link: '/en/guide/translation-config' },
                { text: 'Glossary', link: '/en/guide/glossary' },
                { text: 'Review', link: '/en/guide/review' },
                { text: 'Formats', link: '/en/guide/formats' },
              ],
            },
            {
              text: 'Advanced',
              items: [
                { text: 'Advanced Features', link: '/en/guide/advanced' },
                { text: 'FAQ', link: '/en/guide/faq' },
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
    },

    editLink: {
      pattern: 'https://github.com/MeowSalty/LinguaFlow/edit/main/docs/:path',
    },
  },
})
