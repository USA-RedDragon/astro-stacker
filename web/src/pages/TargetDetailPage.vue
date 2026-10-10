<script setup lang="ts">
import PagePlaceholder from '../components/PagePlaceholder.vue'

defineProps<{ projectId: string }>()
</script>

<template>
  <PagePlaceholder title="Target" :lede="'Project ' + projectId + ': goal, exposure plans, scoring.'" phase="phase 2" />
</template>
