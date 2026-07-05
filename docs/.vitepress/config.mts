import { defineConfig } from 'vitepress'

const base = process.env.DOCS_BASE || '/'

export default defineConfig({
  title: 'LinguaFlow',
  description: '多语言翻译平台',
  base,

  sitemap: {
    hostname: 'https://meowsalty.github.io/LinguaFlow',
  },

  head: [
    ['link', { rel: 'icon', href: `${base}favicon.ico` }],
  ],

  locales: {
    root: {
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
              text: '指南',
              items: [
                { text: '快速开始', link: '/zh/guide/getting-started' },
                { text: '配置', link: '/zh/guide/configuration' },
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
      },
    },
    en: {
      label: 'English',
      lang: 'en-US',
      title: 'LinguaFlow',
      description: 'Multilingual Translation Platform',
      themeConfig: {
        nav: [
          { text: 'Home', link: '/en/' },
          { text: 'Guide', link: '/en/guide/getting-started' },
          { text: 'API', link: '/en/api/' },
        ],
        sidebar: {
          '/en/guide/': [
            {
              text: 'Guide',
              items: [
                { text: 'Getting Started', link: '/en/guide/getting-started' },
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

    footer: {
      message: '基于 MIT 许可证发布',
      copyright: '© 2024-present LinguaFlow',
    },
  },
})
