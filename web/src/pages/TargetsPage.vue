<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { getPlanning, pct, r1, r2, seasonLabel, PRIORITY_INDEX, STATE_INDEX, type Project, type Snapshot } from '../api/planning'
import { editEntity, submitCommand } from '../api/commands'
import { errorToast, notifyCommand, shell, showToast, whenApplies } from '../shell'
import { onEvent } from '../api/events'
import { ago } from '../format'

const router = useRouter()
const snap = ref<Snapshot | null>(null)
const loading = ref(true)
const loadError = ref('')
const f = reactive({ q: '', pri: 'All', state: 'All', kind: 'All' })
const sel = reactive<Record<number, boolean>>({})
const bulkPri = ref('High')

async function load() {
  try {
    snap.value = await getPlanning()
    loadError.value = ''
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

let off: (() => void) | undefined
onMounted(() => {
  load()
  off = onEvent('command', (e: { data?: { status?: string } }) => {
    if (e.data?.status === 'applied') load()
  })
})
onUnmounted(() => off?.())

const projects = computed(() => snap.value?.projects ?? [])

const rows = computed(() => {
  const q = f.q.trim().toLowerCase()
  return projects.value.filter((p) => {
    if (q && !(p.name.toLowerCase().includes(q) || p.description.toLowerCase().includes(q) || p.targets.some((t) => t.name.toLowerCase().includes(q)))) return false
    if (f.pri !== 'All' && p.priority !== f.pri) return false
    if (f.state !== 'All' && p.state !== f.state) return false
    if (f.kind === 'Mosaic' && !p.isMosaic) return false
    if (f.kind === 'Single' && p.isMosaic) return false
    return true
  })
})

const selected = computed(() => projects.value.filter((p) => sel[p.id]))
const allChecked = computed(() => rows.value.length > 0 && rows.value.every((r) => sel[r.id]))
const targetCount = computed(() => rows.value.reduce((a, p) => a + p.targets.length, 0))

function toggleAll(e: Event) {
  const v = (e.target as HTMLInputElement).checked
  rows.value.forEach((r) => (sel[r.id] = v))
}

function clearSel() {
  Object.keys(sel).forEach((k) => delete sel[Number(k)])
}

function pendingFor(p: Project): string {
  const w = shell.waiting.find((c) =>
    (c.objects ?? []).some((o) => (o.entity === 'project' && (o.id === p.id || (!!o.guid && o.guid === p.guid))) || (o.entity === 'target' && p.targets.some((t) => t.id === o.id))),
  )
  if (!w) return ''
  return w.status === 'queued' ? 'queued' : 'applies ' + whenApplies(w)
}

function kindLabel(p: Project): string {
  if (p.isMosaic) return `${p.targets.length} panels`
  return p.targets.length === 1 ? 'Single' : `${p.targets.length} targets`
}

function weakestText(p: Project): string {
  const w = p.weakest
  if (!w) return p.targets.length ? 'No master yet' : 'No targets'
  const m = p.isMosaic && p.weakestTarget ? /(Panel\s*\d+)\s*$/i.exec(p.weakestTarget) : null
  const prefix = m ? m[1] + ' ' : ''
  const g = w.progress
  if (!g) return prefix + w.filter + ' · no master yet'
  if (g.kind === 'depth') return `${prefix}${w.filter} ${r1(g.achieved)} of ${r1(g.goal)} mag/arcsec²`
  return `${prefix}${w.filter} SNR ${r1(g.achieved)} of ${r1(g.goal)}`
}

async function setField(p: Project, field: 'priority' | 'state', value: string) {
  const map = field === 'priority' ? PRIORITY_INDEX : STATE_INDEX
  const before = map[field === 'priority' ? p.priority : p.state]
  const after = map[value]
  if (before === after || after === undefined) return
  try {
    const r = await editEntity('project.edit', { id: p.id, guid: p.guid, name: p.name, changes: [{ field, before, after }] })
    if (field === 'priority') p.priority = value
    else p.state = value
    notifyCommand(r)
  } catch (e) {
    errorToast(e)
    load()
  }
}

async function applyBulkPri() {
  const after = PRIORITY_INDEX[bulkPri.value]
  const changing = selected.value.filter((p) => p.priority !== bulkPri.value)
  if (!changing.length) {
    showToast({ text: 'Nothing to change', sub: `Every selected project is already ${bulkPri.value}.` })
    return
  }
  const items = changing.map((p) => ({ id: p.id, guid: p.guid, name: p.name, changes: [{ field: 'priority', before: PRIORITY_INDEX[p.priority], after }] }))
  try {
    const r = await submitCommand('project.batchedit', { items })
    changing.forEach((p) => (p.priority = bulkPri.value))
    notifyCommand(r)
  } catch (e) {
    errorToast(e)
  }
}

function applySet() {
  const ids = selected.value.flatMap((p) => p.targets.map((t) => t.id))
  router.push({ name: 'templates', query: { targets: ids.join(',') } })
}

function open(p: Project) {
  router.push({ name: 'target', params: { projectId: String(p.id) } })
}
</script>

<template>
  <main class="page wide">
    <div class="page-head">
      <div>
        <h1>Targets</h1>
        <p class="lede">
          Every project on the scheduler. Change priority or state right in the row; it applies when the current exposure ends. Open a project for its goal, plans and scoring.
        </p>
      </div>
      <RouterLink :to="{ name: 'add' }" class="btn primary">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" aria-hidden="true"><path d="M12 5v14M5 12h14" /></svg>
        Add target
      </RouterLink>
    </div>

    <div class="row" style="align-items: flex-end; gap: 0.75rem">
      <label class="field" style="flex: 1 1 16rem">
        <span>Search name or catalogue ID</span>
        <input v-model="f.q" class="input" type="search" placeholder="Cygnis, IC 1318, M 31" />
      </label>
      <label class="field">
        <span>Priority</span>
        <select v-model="f.pri" class="input"><option>All</option><option>High</option><option>Normal</option><option>Low</option></select>
      </label>
      <label class="field">
        <span>State</span>
        <select v-model="f.state" class="input"><option>All</option><option>Active</option><option>Inactive</option><option>Closed</option><option>Draft</option></select>
      </label>
      <label class="field">
        <span>Kind</span>
        <select v-model="f.kind" class="input"><option>All</option><option>Single</option><option>Mosaic</option></select>
      </label>
    </div>

    <div v-if="selected.length" role="region" aria-label="Bulk edit" class="bulk">
      <div style="margin-right: auto; min-width: 0">
        <div style="font-weight: 600">{{ selected.length }} selected</div>
        <div class="xsmall muted">{{ selected.map((p) => p.name).join(', ') }}</div>
      </div>
      <label class="row small">
        <span class="muted">Priority</span>
        <select v-model="bulkPri" class="input"><option>High</option><option>Normal</option><option>Low</option></select>
      </label>
      <button type="button" class="btn" @click="applyBulkPri">Set priority</button>
      <button type="button" class="btn primary" @click="applySet">Apply an exposure set…</button>
      <button type="button" class="btn link small" @click="clearSel">Clear</button>
    </div>

    <section class="card" aria-labelledby="tl-h">
      <div class="spread">
        <h2 id="tl-h">{{ rows.length }} {{ rows.length === 1 ? 'project' : 'projects' }} · {{ targetCount }} targets</h2>
        <span class="xsmall muted">Progress is the weakest filter against its faint-SNR goal; for a mosaic, its weakest panel.</span>
      </div>
      <p v-if="loading" class="empty">Loading the scheduler's projects…</p>
      <p v-else-if="loadError" class="empty" style="color: var(--bad)">Could not load projects: {{ loadError }}</p>
      <div v-else class="scroll-x" style="border: 1px solid var(--border); border-radius: 0.5rem">
        <table class="grid num" style="min-width: 62rem">
          <thead>
            <tr>
              <th><input type="checkbox" :checked="allChecked" aria-label="Select every shown project" @change="toggleAll" /></th>
              <th>Project</th>
              <th>Priority</th>
              <th>State</th>
              <th>Exposure set</th>
              <th style="min-width: 11rem">Toward goal</th>
              <th>Season</th>
              <th>Last sub</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in rows" :key="p.id" :style="{ background: sel[p.id] ? 'var(--sel)' : 'transparent' }">
              <td><input v-model="sel[p.id]" type="checkbox" :aria-label="'Select ' + p.name" /></td>
              <td style="white-space: nowrap">
                <button type="button" class="lnk plain" @click="open(p)">{{ p.name }}</button>
                <div class="xsmall muted">
                  {{ kindLabel(p) }}<span v-if="pendingFor(p)" style="color: var(--warn)"> · edit {{ pendingFor(p) }}</span>
                </div>
              </td>
              <td>
                <label :for="'pri-' + p.id" class="sr-only">Priority of {{ p.name }}</label>
                <select :id="'pri-' + p.id" class="input" style="height: 2rem" :value="p.priority" @change="setField(p, 'priority', ($event.target as HTMLSelectElement).value)">
                  <option>High</option><option>Normal</option><option>Low</option>
                </select>
              </td>
              <td>
                <label :for="'st-' + p.id" class="sr-only">State of {{ p.name }}</label>
                <select :id="'st-' + p.id" class="input" style="height: 2rem" :value="p.state" @change="setField(p, 'state', ($event.target as HTMLSelectElement).value)">
                  <option>Active</option><option>Inactive</option><option>Closed</option><option v-if="p.state === 'Draft'">Draft</option>
                </select>
              </td>
              <td style="white-space: nowrap">{{ p.exposureSet }}</td>
              <td>
                <div class="row" style="flex-wrap: nowrap">
                  <div class="bar"><div :style="{ width: pct(p.progress) }" /></div>
                  <span style="font-weight: 600; width: 2.75rem; text-align: right">{{ p.weakest?.progress ? pct(p.progress) : '—' }}</span>
                </div>
                <div class="xsmall muted" style="white-space: nowrap">{{ weakestText(p) }}</div>
              </td>
              <td style="white-space: nowrap">
                <span class="badge" :class="{ violet: seasonLabel(p.season).cls === 'violet' }">{{ seasonLabel(p.season).label }}</span>
                <div class="xsmall muted">Novelty {{ r2(p.novelty) }} · Rarity {{ r2(p.rarity) }}</div>
              </td>
              <td class="muted" style="white-space: nowrap">{{ p.lastSub ? ago(p.lastSub, shell.now) : 'No subs yet' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-if="!loading && !loadError && rows.length === 0" class="empty">No project matches these filters.</p>
      <p class="xsmall muted" style="margin: 0">Novelty and Rarity are this project's scores for the two new planner rules. Open a project's Scoring tab to see how they add up.</p>
    </section>
  </main>
</template>

<style scoped>
.bulk {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
  align-items: center;
  border: 1px solid var(--foreground);
  border-radius: 14px;
  padding: 0.875rem 1.25rem;
  background: var(--card);
}
.bar {
  flex: 1;
  height: 0.4375rem;
  border-radius: 999px;
  background: var(--secondary);
  overflow: hidden;
  min-width: 4rem;
}
.bar > div {
  height: 100%;
  background: var(--foreground);
}
.plain {
  border: 0;
  background: transparent;
  padding: 0;
  font-weight: 600;
  text-align: left;
}
.badge.violet {
  border: 0;
  background: var(--violet-bg);
  color: var(--violet);
}
</style>
