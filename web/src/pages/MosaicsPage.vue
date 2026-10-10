<script setup lang="ts">
import PagePlaceholder from '../components/PagePlaceholder.vue'

defineProps<{ projectId?: string }>()
</script>

<template>
  <PagePlaceholder title="Mosaics" lede="Panels, seams, seasons, adoption." phase="phase 3" />
</template>
