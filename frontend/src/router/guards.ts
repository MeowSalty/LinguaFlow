import type { Router } from 'vue-router'

import { setUnauthorizedHandler } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { useServiceStore } from '@/stores/service'

const PUBLIC_PATHS = new Set(['/login', '/register', '/service'])
const AUTH_ENTRY_PATHS = new Set(['/login', '/register'])

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
}
