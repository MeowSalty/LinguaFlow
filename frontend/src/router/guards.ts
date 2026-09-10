import type { Router } from 'vue-router'

import { setUnauthorizedHandler } from '@/api/client'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import { useServiceStore } from '@/stores/service'

const PUBLIC_PATHS = new Set(['/login', '/register', '/service'])
const AUTH_ENTRY_PATHS = new Set(['/login', '/register'])

const ROUTE_TITLES: Record<string, string> = {
  '/': 'nav.dashboard',
  '/about': 'nav.about',
  '/admin/': 'nav.adminDashboard',
  '/admin/audit-logs': 'nav.adminAuditLogs',
  '/admin/settings': 'nav.adminSettings',
  '/admin/users': 'nav.adminUsers',
  '/backends': 'nav.backends',
  '/bootstrap-prompt-templates': 'nav.bootstrapPromptTemplates',
  '/changelog': 'nav.changelog',
  '/execution-plan-templates': 'nav.executionPlanTemplates',
  '/execution-profiles': 'nav.executionProfiles',
  '/login': 'login.title',
  '/projects': 'nav.projects',
  '/projects/[projectId]': 'nav.projects',
  '/prompt-templates': 'nav.promptTemplates',
  '/prune-prompt-templates': 'nav.prunePromptTemplates',
  '/register': 'register.title',
  '/service': 'service.title',
  '/stats': 'nav.stats',
  '/tools/epub-rotate': 'nav.epubRotate',
  '/[...all]': 'notFound.title',
}

const applyDocumentTitle = (routeName: string | symbol | null | undefined): void => {
  const key = typeof routeName === 'string' ? ROUTE_TITLES[routeName] : undefined
  document.title = key ? `${t(key)} · LinguaFlow` : 'LinguaFlow'
}

export const installRouterGuards = (router: Router): void => {
  const service = useServiceStore()
  const auth = useAuthStore()

  setUnauthorizedHandler(() => {
    auth.clearSession()
    if (!service.isLocal && router.currentRoute.value.path !== '/login') {
      router.push('/login')
    }
  })

  router.beforeEach((to) => {
    const service = useServiceStore()
    const auth = useAuthStore()

    if (!service.isAppReady) {
      return false
    }

    const isPublic = to.meta.public === true || PUBLIC_PATHS.has(to.path)
    const forceService = to.query.force === '1'

    if (service.isLocal) {
      if (AUTH_ENTRY_PATHS.has(to.path)) {
        const redirect = typeof to.query.redirect === 'string' ? to.query.redirect : null
        return redirect ? { path: redirect } : { path: '/' }
      }

      if (to.path === '/service' && !forceService) {
        const redirect = typeof to.query.redirect === 'string' ? to.query.redirect : null
        return redirect ? { path: redirect } : { path: '/' }
      }

      if (!auth.user && !isPublic) {
        const redirect = typeof to.query.redirect === 'string' ? to.query.redirect : null
        return redirect ? { path: redirect } : { path: '/' }
      }

      return undefined
    }

    if (!service.hasSelected && to.path !== '/service') {
      return {
        path: '/service',
        query: to.fullPath !== '/' ? { redirect: to.fullPath } : {},
      }
    }

    if (auth.isAuthenticated && AUTH_ENTRY_PATHS.has(to.path)) {
      const redirect = typeof to.query.redirect === 'string' ? to.query.redirect : null
      return redirect ? { path: redirect } : { path: '/' }
    }

    if (!auth.isAuthenticated && !isPublic) {
      return {
        path: '/login',
        query: { redirect: to.fullPath },
      }
    }

    return undefined
  })

  router.afterEach((to) => {
    applyDocumentTitle(to.name)
  })
}
