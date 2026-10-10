<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { navDiscover, navOperate, navPlan, type NavItem } from '../router'
import { shell, indicator } from '../shell'
import { toggleTheme } from '../theme'
import StatusPopover from './StatusPopover.vue'

const route = useRoute()
const ind = computed(indicator)
const groups: { label: string; items: NavItem[] }[] = [
  { label: 'Operate', items: navOperate },
  { label: 'Plan', items: navPlan },
  { label: 'Discover', items: navDiscover },
]
const active = (name: string) => route.name === name || route.meta.nav === name
</script>

<template>
  <header class="topbar">
    <RouterLink :to="{ name: 'start' }" class="brand lnk">Astro Processing</RouterLink>
    <nav aria-label="Main" class="nav">
      <RouterLink
        :to="{ name: 'start' }"
        class="lnk nav-start"
        :aria-current="active('start') ? 'page' : undefined"
        :style="{ fontWeight: active('start') ? 700 : 400 }"
        >Start</RouterLink
      >
      <template v-for="g in groups" :key="g.label">
        <span class="nav-group">{{ g.label }}</span>
        <RouterLink
          v-for="it in g.items"
          :key="it.name"
          :to="{ name: it.name }"
          class="lnk nav-item"
          :aria-current="active(it.name) ? 'page' : undefined"
          :style="{ fontWeight: active(it.name) ? 700 : 400 }"
          >{{ it.label }}</RouterLink
        >
      </template>
    </nav>
    <div class="right">
      <button
        type="button"
        class="status-btn num"
        aria-haspopup="dialog"
        :aria-expanded="shell.statusOpen ? 'true' : 'false'"
        :style="{ background: ind.bg, color: ind.tone }"
        @click="shell.statusOpen = !shell.statusOpen"
      >
        <svg width="8" height="8" viewBox="0 0 8 8" aria-hidden="true"><circle cx="4" cy="4" r="4" :fill="ind.dot" /></svg>
        {{ ind.label }}
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m6 9 6 6 6-6" /></svg>
      </button>
      <button type="button" class="theme-btn" aria-label="Toggle colour mode" @click="toggleTheme">
        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z" /></svg>
      </button>
      <StatusPopover v-if="shell.statusOpen" />
    </div>
  </header>
</template>

<style scoped>
.topbar {
  position: sticky;
  top: 0;
  z-index: 20;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 0.5rem 1rem;
  min-height: 3em;
  padding: 0.5em 1rem;
  background: var(--secondary);
}
.brand {
  font-size: 1rem;
  font-weight: 600;
}
.nav {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  row-gap: 0.25rem;
  font-size: 0.95rem;
}
.nav-start {
  padding: 0 0.85rem;
}
.nav-group {
  display: inline-flex;
  align-items: center;
  border-left: 1px solid #444;
  padding-left: 0.85rem;
  font-size: 0.6875rem;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--muted-foreground);
}
.nav-item {
  padding: 0 0.6rem;
}
.right {
  position: relative;
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
.status-btn {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  height: 2rem;
  padding: 0 0.625rem;
  border: 1px solid var(--border);
  border-radius: 999px;
  font-size: 0.75rem;
  font-weight: 500;
}
.theme-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 2.25rem;
  height: 2.25rem;
  border: 0;
  border-radius: 0.5rem;
  background: transparent;
}
</style>
