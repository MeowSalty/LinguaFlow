<script setup lang="ts"></script>

<template>
  <NConfigProvider>
    <NMessageProvider>
      <NLayout class="app-layout">
        <NLayoutHeader bordered class="app-header">
          <RouterLink to="/" class="brand">LinguaFlow</RouterLink>
          <nav class="nav-links" aria-label="主导航">
            <RouterLink to="/">首页</RouterLink>
            <RouterLink to="/about">关于</RouterLink>
          </nav>
        </NLayoutHeader>

        <NLayoutContent class="app-content">
          <RouterView />
        </NLayoutContent>
      </NLayout>
    </NMessageProvider>
  </NConfigProvider>
</template>

<style scoped>
.app-layout {
  min-height: 100vh;
}

.app-header {
  display: flex;
  align-items: center;
  gap: 32px;
  height: 64px;
  padding: 0 32px;
}

.brand {
  color: #18a058;
  font-size: 20px;
  font-weight: 700;
  text-decoration: none;
}

.nav-links {
  display: flex;
  gap: 16px;
}

.nav-links a {
  color: inherit;
  text-decoration: none;
}

.nav-links a.router-link-active {
  color: #18a058;
  font-weight: 600;
}

.app-content {
  padding: 48px 32px;
}
</style>
