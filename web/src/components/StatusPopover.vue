<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { shell, cancel, exposureEnd } from '../shell'
import { ago, hm, mmss } from '../format'

const router = useRouter()
const s = computed(() => shell.scheduler)
const pending = computed(() => shell.waiting.filter((c) => c.status === 'pending'))
const queued = computed(() => shell.waiting.filter((c) => c.status === 'queued'))

const connLabel = computed(() => {
  switch (s.value.reachable) {
    case 'online':
      return 'Reachable' + (s.value.last_answer ? ' · last answer ' + ago(s.value.last_answer, shell.now) : '')
    case 'offline':
      return s.value.since ? 'Not answering since ' + hm(s.value.since) : 'Not answering'
    case 'unconfigured':
      return 'No scheduler API configured'
    default:
      return 'Checking'
  }
})

const exposureLine = computed(() => {
  const e = s.value.exposure
  if (s.value.paused) return 'Paused · no exposure running'
  if (!e || !e.ends_at) return s.value.state ? s.value.state : 'No exposure running'
  const left = (new Date(e.ends_at).getTime() - shell.now) / 1000
  const n = e.number ? 'Exposure ' + e.number + ' · ' : ''
  return `${n}${e.filter} ${Math.round(e.seconds)} s · ends ${hm(e.ends_at)} (${mmss(left)} left)`
})

const when = (r: { status: string; created_at: string; applies_at?: string }) =>
  r.status === 'queued' ? 'queued ' + hm(r.created_at) : 'applies ' + (r.applies_at ? hm(r.applies_at) : exposureEnd() || 'at the next plan')

function openHistory() {
  shell.statusOpen = false
  router.push({ name: 'history' })
}
</script>

<template>
  <div role="dialog" class="pop" aria-label="Observatory connection and pending changes">
    <div class="spread">
      <span class="pop-title">Observatory PC</span>
      <button type="button" class="btn ghost sm" aria-label="Close" @click="shell.statusOpen = false">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" aria-hidden="true"><path d="M18 6 6 18M6 6l12 12" /></svg>
      </button>
    </div>
    <div class="facts num">
      <span class="muted">Connection</span><span>{{ connLabel }}</span>
      <span class="muted">Camera</span><span>{{ exposureLine }}</span>
      <template v-if="s.web_editing === false">
        <span class="muted">Editing</span><span>Web editing is off on the PC</span>
      </template>
    </div>
    <p class="muted xsmall" style="margin: 0">
      Edits apply when the current exposure ends, never mid-sub and never after a minimum-time window. If the PC doesn't
      answer, they wait here in order and go out when it does.
    </p>
    <div v-if="pending.length" class="list">
      <span class="list-title">Waiting for the end of this exposure</span>
      <div v-for="c in pending" :key="c.id" class="list-row">
        <span>{{ c.title }} <span class="muted">· {{ when(c) }}</span></span>
        <button type="button" class="btn sm" @click="cancel(c.id)">Cancel</button>
      </div>
    </div>
    <div v-if="queued.length" class="list">
      <span class="list-title" style="color: var(--warn)">Queued offline</span>
      <div v-for="c in queued" :key="c.id" class="list-row">
        <span>{{ c.title }} <span class="muted">· {{ when(c) }}</span></span>
        <button type="button" class="btn sm" @click="cancel(c.id)">Discard</button>
      </div>
    </div>
    <p v-if="!pending.length && !queued.length" style="margin: 0">Nothing is waiting. Every edit has reached the scheduler.</p>
    <div class="row">
      <button type="button" class="btn link xsmall" @click="openHistory">Open History</button>
    </div>
  </div>
</template>

<style scoped>
.pop {
  position: absolute;
  right: 0;
  top: calc(100% + 0.5rem);
  z-index: 40;
  width: min(24rem, calc(100vw - 2rem));
  border: 1px solid var(--border);
  border-radius: 14px;
  background: var(--card);
  padding: 1rem;
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  box-shadow: 0 20px 50px oklch(0 0 0 / 0.45);
  font-size: 0.8125rem;
}
@media (max-width: 640px) {
  .pop {
    position: fixed;
    left: 1rem;
    right: 1rem;
    top: 4.5rem;
    width: auto;
    max-height: calc(100vh - 6rem);
    overflow: auto;
  }
}
.pop-title {
  font-weight: 600;
  font-size: 0.9375rem;
}
.facts {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 0.25rem 0.75rem;
}
.list {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
}
.list-title {
  font-weight: 600;
}
.list-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 0.5rem;
  border-top: 1px solid var(--border);
  padding-top: 0.25rem;
}
</style>
