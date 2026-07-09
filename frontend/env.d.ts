/// <reference types="vite/client" />
/// <reference types="vue-router/auto" />

declare module '*.vue' {
  import type { DefineComponent } from 'vue'

  const component: DefineComponent
  export default component
}

declare module '~icons/*' {
  import type { DefineComponent } from 'vue'

  const component: DefineComponent
  export default component
}
