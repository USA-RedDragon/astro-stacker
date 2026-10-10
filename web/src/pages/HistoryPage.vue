<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { listCommands, type CommandRecord } from '../api/commands'
import { onEvent } from '../api/events'
import { hm, shortDate, show } from '../format'
import { historyBadge, undoState } from '../plan'
import { cancel, shell, undo, whenApplies } from '../shell'

type Who = 'all' | 'web' | 'app' | 'observatory'

const who = ref<Who>('all')
const category = ref('')
const q = ref('')
const records = ref<CommandRecord[]>([])
const loading = ref(false)
const error = ref('')
const busy = ref<string | null>(null)

const categories = [
  { value: '', label: 'All kinds' },
  { value: 'control', label: 'Skips and pauses' },
  { value: 'priority', label: 'Priority, state, active' },
  { value: 'goals', label: 'Goals' },
  { value: 'rules', label: 'Rule weights' },
  { value: 'plans', label: 'Exposure plans' },
  { value: 'templates', label: 'Templates' },
  { value: 'created', label: 'Created' },
  { value: 'matching', label: 'Name matches' },
  { value: 'adoption', label: 'Mosaic adoption' },
]

let seq = 0
async function load() {
  const mine = ++seq
  loading.value = true
  try {
    const out = await listCommands({ category: category.value || undefined, q: q.value.trim() || undefined, limit: 300 })
    if (mine !== seq) return
    records.value = out
    error.value = ''
  } catch (e) {
    if (mine !== seq) return
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    if (mine === seq) loading.value = false
  }
}

let qTimer: ReturnType<typeof setTimeout> | undefined
watch(category, load)
watch(q, () => {
  if (qTimer) clearTimeout(qTimer)
  qTimer = setTimeout(load, 300)
})

function matchesWho(r: CommandRecord): boolean {
  switch (who.value) {
    case 'web':
      return r.destination === 'observatory' && r.author !== 'observatory'
    case 'app':
      return r.destination === 'app'
    case 'observatory':
      return r.author === 'observatory'
  }
  return true
}

function matchesFilters(r: CommandRecord): boolean {
  if (category.value && r.category !== category.value) return false
  const s = q.value.trim().toLowerCase()
  if (s) {
    const text = [r.title, r.author, ...(r.objects ?? []).map((o) => o.name)].join(' ').toLowerCase()
    if (!text.includes(s)) return false
  }
  return true
}

let off: (() => void) | undefined
onMounted(() => {
  load()
  off = onEvent('command', (e: { data?: CommandRecord }) => {
    const r = e.data
    if (!r) return
    const i = records.value.findIndex((x) => x.id === r.id)
    if (i >= 0) records.value[i] = r
    else if (matchesFilters(r)) records.value.unshift(r)
  })
})
onUnmounted(() => {
  if (off) off()
  if (qTimer) clearTimeout(qTimer)
})

function clear() {
  who.value = 'all'
  category.value = ''
  q.value = ''
}

const shown = computed(() => records.value.filter(matchesWho))

function whoLine(r: CommandRecord): string {
  const parts = [r.author]
  if (r.destination === 'app') parts.push('app only')
  else if (r.transport === 'api') parts.push('sent over the API')
  else if (r.transport === 'queue') parts.push('sent through the database queue')
  if (r.attempts > 1) parts.push(`${r.attempts} attempts`)
  return parts.join(' · ')
}

function badgeOf(r: CommandRecord) {
  return historyBadge(r, r.status === 'pending' ? whenApplies(r) : '')
}

const entries = computed(() =>
  shown.value.map((r) => {
    const multi = new Set((r.diffs ?? []).map((d) => d.object.name)).size > 1
    return {
      r,
      badge: badgeOf(r),
      undo: undoState(r),
      diffs: (r.diffs ?? []).map((d) => ({ field: multi ? `${d.object.name} · ${d.field}` : d.field, before: show(d.before), after: show(d.after) })),
      note: [r.message, r.note].filter(Boolean).join(' · '),
    }
  }),
)

async function act(r: CommandRecord, isCancel: boolean) {
  busy.value = r.id
  try {
    const out = isCancel ? await cancel(r.id) : await undo(r.id)
    const i = records.value.findIndex((x) => x.id === out.id)
    if (i >= 0) records.value[i] = out
    else records.value.unshift(out)
  } catch {
    return
  } finally {
    busy.value = null
  }
}

const pendingN = computed(() => shell.waiting.filter((c) => c.status === 'pending').length)
const queuedN = computed(() => shell.waiting.filter((c) => c.status === 'queued').length)
const waitingLabel = computed(() => {
  const parts: string[] = []
  if (pendingN.value) {
    parts.push(`${pendingN.value} ${pendingN.value === 1 ? 'change applies' : 'changes apply'} ${whenApplies()}`)
  }
  if (queuedN.value) parts.push(shell.scheduler.reachable === 'offline' ? `${queuedN.value} queued until the PC answers` : `${queuedN.value} sent via the backup queue`)
  return parts.join(' · ')
})
</script>

<template>
  <main class="page wide">
    <div>
      <h1 class="h1">History</h1>
      <p class="lede" style="max-width: 76ch">
        Every change to the scheduler. When web editing is on, the Target Scheduler editor on the observatory PC is read-only, so every entry has a
        before-value. Undo sends the reverse change; it never rewrites the past.
      </p>
    </div>

    <div v-if="waitingLabel" role="status" class="waiting">
      <span>{{ waitingLabel }}</span>
      <button type="button" class="btn sm" @click="shell.statusOpen = true">Show what is waiting</button>
    </div>

    <div class="filters">
      <label for="h-who" class="field">
        <span>Who</span>
        <select id="h-who" v-model="who" class="input">
          <option value="all">Everyone</option>
          <option value="web">Web</option>
          <option value="app">App only: matches and adoption</option>
          <option value="observatory">The observatory PC</option>
        </select>
      </label>
      <label for="h-what" class="field">
        <span>Change</span>
        <select id="h-what" v-model="category" class="input">
          <option v-for="c in categories" :key="c.value" :value="c.value">{{ c.label }}</option>
        </select>
      </label>
      <label for="h-obj" class="field grow">
        <span>Project, target or template</span>
        <input id="h-obj" v-model="q" type="search" class="input" placeholder="Cygnis Loop" />
      </label>
      <button type="button" class="btn link small" style="height: 2.25rem" @click="clear">Clear filters</button>
    </div>

    <section aria-labelledby="hist-h" class="card list">
      <h2 id="hist-h" class="sr-only">Changes</h2>
      <p v-if="error" class="empty">Could not load history: {{ error }}</p>
      <ol v-else class="entries">
        <li v-for="e in entries" :key="e.r.id">
          <div class="small num">
            <div style="font-weight: 600">{{ hm(e.r.created_at) }}</div>
            <div class="muted">{{ shortDate(e.r.created_at) }}</div>
            <div class="muted xsmall">#{{ e.r.id.slice(0, 8) }}</div>
          </div>
          <div class="body">
            <div class="row" style="font-size: 0.875rem">
              <span style="font-weight: 600">{{ e.r.title }}</span>
              <span class="badge" :class="e.badge.tone">{{ e.badge.label }}</span>
            </div>
            <div class="xsmall muted">{{ whoLine(e.r) }}</div>
            <div v-if="e.diffs.length" class="row num diffs">
              <span v-for="(d, i) in e.diffs" :key="i" class="diff">
                <span class="muted">{{ d.field }}</span>
                <del>{{ d.before }}</del>
                <span aria-hidden="true">→</span>
                <ins>{{ d.after }}</ins>
              </span>
            </div>
            <div v-if="e.note" class="xsmall muted">{{ e.note }}</div>
          </div>
          <div class="undo">
            <button
              type="button"
              class="btn sm"
              :aria-label="e.undo.label + ': ' + e.r.title"
              :disabled="!e.undo.enabled || busy === e.r.id"
              @click="act(e.r, e.undo.cancel)"
            >
              {{ e.undo.label }}
            </button>
            <span class="undo-note">{{ e.undo.note }}</span>
          </div>
        </li>
      </ol>
      <p v-if="!error && !entries.length" class="empty">{{ loading ? 'Loading.' : 'No change matches these filters.' }}</p>
    </section>
  </main>
</template>

<style scoped>
.h1 {
  margin: 0;
  font-size: 1.5rem;
  font-weight: 600;
  line-height: 1.3;
}
.waiting {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  align-items: center;
  gap: 0.75rem;
  border: 1px solid var(--warn);
  background: var(--warn-bg);
  border-radius: 0.625rem;
  padding: 0.625rem 1rem;
  font-size: 0.8125rem;
}
.filters {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
  align-items: flex-end;
}
.grow {
  flex: 1 1 14rem;
}
.grow .input {
  min-width: 0;
}
.list {
  padding: 0.5rem 1.5rem;
  gap: 0;
}
.entries {
  list-style: none;
  margin: 0;
  padding: 0;
}
.entries li {
  display: grid;
  grid-template-columns: minmax(0, 7rem) minmax(0, 1fr) auto;
  gap: 0.5rem 1.25rem;
  padding: 1rem 0;
  border-bottom: 1px solid var(--border);
  align-items: start;
}
.entries li:last-child {
  border-bottom: 0;
}
.body {
  display: flex;
  flex-direction: column;
  gap: 0.375rem;
  min-width: 0;
}
.diffs {
  gap: 0.375rem;
  font-size: 0.8125rem;
}
.diff {
  display: inline-flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.375rem;
  border: 1px solid var(--border);
  border-radius: 0.375rem;
  padding: 0.125rem 0.5rem;
}
del {
  background: var(--del-bg);
  border-radius: 0.25rem;
  padding: 0 0.25rem;
}
ins {
  background: var(--add-bg);
  border-radius: 0.25rem;
  padding: 0 0.25rem;
  text-decoration: none;
}
.undo {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 0.25rem;
}
.undo .btn {
  height: 2rem;
  padding: 0 0.875rem;
  font-size: 0.8125rem;
}
.undo-note {
  font-size: 0.6875rem;
  color: var(--muted-foreground);
  text-align: right;
  max-width: 10rem;
}
@media (max-width: 640px) {
  .entries li {
    grid-template-columns: minmax(0, 1fr);
  }
  .undo {
    align-items: flex-start;
  }
  .undo-note {
    text-align: left;
    max-width: none;
  }
}
</style>
