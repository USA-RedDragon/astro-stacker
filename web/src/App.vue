<script setup lang="ts">
import { onMounted } from 'vue'
import TopBar from './components/TopBar.vue'
import ToastHost from './components/ToastHost.vue'
import SiteFooter from './components/SiteFooter.vue'
import { startShell } from './shell'

onMounted(startShell)
</script>

<template>
  <TopBar />
  <RouterView v-slot="{ Component }">
    <component :is="Component" />
  </RouterView>
  <SiteFooter />
  <ToastHost />
</template>
