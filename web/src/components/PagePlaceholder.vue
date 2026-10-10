<script setup lang="ts">
defineProps<{ title: string; lede?: string; phase?: string }>()
</script>

<template>
  <main class="page">
    <div class="page-head">
      <div>
        <h1>{{ title }}</h1>
        <p v-if="lede" class="lede">{{ lede }}</p>
      </div>
    </div>
    <section class="card">
      <p class="empty">This page is not built yet{{ phase ? ' (' + phase + ')' : '' }}.</p>
      <slot />
    </section>
  </main>
</template>
