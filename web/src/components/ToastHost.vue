<script setup lang="ts">
import { shell, closeToast, undo } from '../shell'

function doUndo() {
  const id = shell.toast?.undoId
  closeToast()
  if (id) undo(id).catch(() => undefined)
}
</script>

<template>
  <div v-if="shell.toast" :key="shell.toast.at" role="status" aria-live="polite" class="toast">
    <div class="toast-body">
      <div class="toast-text" :style="{ color: shell.toast.tone ? `var(--${shell.toast.tone})` : undefined }">{{ shell.toast.text }}</div>
      <div v-if="shell.toast.sub" class="toast-sub">{{ shell.toast.sub }}</div>
    </div>
    <button v-if="shell.toast.undoId" type="button" class="btn sm" @click="doUndo">Undo</button>
    <button type="button" class="btn ghost sm" aria-label="Dismiss" @click="closeToast">
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" aria-hidden="true"><path d="M18 6 6 18M6 6l12 12" /></svg>
    </button>
  </div>
</template>

<style scoped>
.toast {
  position: fixed;
  left: 50%;
  bottom: calc(1rem + env(safe-area-inset-bottom, 0px));
  transform: translateX(-50%);
  z-index: 60;
  display: flex;
  align-items: flex-start;
  gap: 0.75rem;
  width: min(30rem, calc(100vw - 2rem));
  border: 1px solid var(--border);
  border-radius: 0.75rem;
  background: var(--secondary);
  padding: 0.75rem 1rem;
  box-shadow: 0 12px 32px oklch(0 0 0 / 0.4);
}
@media (prefers-reduced-motion: no-preference) {
  .toast {
    animation: toastIn 160ms ease-out;
  }
}
@keyframes toastIn {
  from {
    opacity: 0;
    margin-bottom: -8px;
  }
  to {
    opacity: 1;
    margin-bottom: 0;
  }
}
.toast-body {
  flex: 1;
  min-width: 0;
}
.toast-text {
  font-weight: 600;
  font-size: 0.875rem;
}
.toast-sub {
  font-size: 0.75rem;
  color: var(--muted-foreground);
}
</style>
