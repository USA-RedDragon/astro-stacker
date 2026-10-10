<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  addLink,
  decideMatch,
  filterColour,
  filterOrder,
  getCatalogue,
  getMatches,
  getObject,
  getOverview,
  hours,
  label,
  size,
  typeLabel,
  type CatalogueEntry,
  type Decision,
  type ObjectDetail,
  type Overview,
  type ReviewItem,
} from '../api/discover'
import { errorToast, notifyCommand } from '../shell'
import { hm } from '../format'

const route = useRoute()
const router = useRouter()

const overview = ref<Overview | null>(null)
const entries = ref<CatalogueEntry[]>([])
const matches = ref<ReviewItem[]>([])
const loading = ref(true)
const listLoading = ref(false)
const failed = ref('')
const tab = ref(typeof route.query.list === 'string' ? route.query.list : 'messier')
const tonightOnly = ref(false)
const status = ref('All')
const pick = ref<CatalogueEntry | null>(null)
const detail = ref<ObjectDetail | null>(null)
const busy = ref('')
const sourcesOpen = ref(false)

const statusLabel: Record<string, string> = { done: 'Done', 'in-progress': 'In progress', 'not-started': 'Not started' }

const current = computed(() => overview.value?.catalogues.find((c) => c.key === tab.value))

async function loadOverview() {
  try {
    overview.value = await getOverview()
    failed.value = ''
    if (!overview.value.catalogues.some((c) => c.key === tab.value) && overview.value.catalogues.length) {
      tab.value = overview.value.catalogues[0]!.key
    }
  } catch (e) {
    failed.value = e instanceof Error ? e.message : String(e)
  }
}

async function loadList() {
  listLoading.value = true
  try {
    entries.value = await getCatalogue(tab.value)
  } catch (e) {
    errorToast(e, 'Could not load the catalogue')
    entries.value = []
  } finally {
    listLoading.value = false
  }
}

async function loadMatches() {
  try {
    matches.value = await getMatches(true)
  } catch (e) {
    errorToast(e, 'Could not load the name matches')
  }
}

onMounted(async () => {
  await Promise.all([loadOverview(), loadMatches()])
  await loadList()
  loading.value = false
})

watch(tab, (t) => {
  pick.value = null
  detail.value = null
  router.replace({ query: { ...route.query, list: t } })
  loadList()
})

function choose(e: CatalogueEntry) {
  if (pick.value?.object.id === e.object.id) {
    pick.value = null
    detail.value = null
    return
  }
  pick.value = e
  detail.value = null
  getObject(e.object.id)
    .then((d) => {
      if (pick.value?.object.id === e.object.id) detail.value = d
    })
    .catch(() => undefined)
}

function hidden(e: CatalogueEntry): boolean {
  if (tonightOnly.value && !e.tonight?.up) return true
  return status.value !== 'All' && statusLabel[e.status] !== status.value
}

function cellStyle(e: CatalogueEntry) {
  const s = e.status
  return {
    background: s === 'done' ? 'var(--done)' : s === 'in-progress' ? 'var(--prog-bg)' : 'transparent',
    color: s === 'done' ? 'var(--ink-dark)' : s === 'in-progress' ? 'var(--foreground)' : 'var(--muted-foreground)',
    border: s === 'done' ? '1px solid var(--done)' : s === 'in-progress' ? '1px solid var(--prog)' : '1px dashed var(--border)',
    boxShadow: e.tonight?.up ? 'inset 0 -3px 0 var(--warn)' : 'none',
    opacity: hidden(e) ? 0.18 : 1,
    outline: pick.value?.object.id === e.object.id ? '2px solid var(--foreground)' : '0 solid transparent',
  }
}

const cellText = (e: CatalogueEntry) => (tab.value === 'herschel400' ? String(e.index) : e.label)

const wideCells = computed(() => entries.value.some((e) => e.label.length > 8))

const upNotStarted = computed(() =>
  entries.value
    .filter((e) => e.status === 'not-started' && e.tonight?.up)
    .sort((a, b) => (b.tonight?.hours ?? 0) - (a.tonight?.hours ?? 0))
    .slice(0, 8),
)

function window(e: CatalogueEntry): string {
  const t = e.tonight
  if (!t) return 'visibility unknown until the site is known'
  if (!t.up) return t.peakAlt > 0 ? `peaks at ${Math.round(t.peakAlt)}°, not up long enough tonight` : 'not up tonight'
  return `up ${hm(t.start)} – ${hm(t.end)}, ${hours(t.hours)} above ${overview.value?.night?.minAltitude ?? 30}°`
}

function pickStatus(e: CatalogueEntry): string {
  const s = statusLabel[e.status] ?? e.status
  const subj = e.subjects.filter((x) => x.status !== 'rejected' && x.status !== 'suggested')
  if (!subj.length) return s
  return `${s} · ${subj.map((x) => x.name + (x.state ? ` (${x.state})` : '')).join(', ')}`
}

const pickHours = computed(() => {
  const h = pick.value?.hours ?? {}
  return Object.keys(h)
    .sort(filterOrder)
    .map((f) => ({ f, h: h[f]!, colour: filterColour(f) }))
})

const pickProject = computed(() => pick.value?.subjects.find((s) => s.projectId && s.status !== 'rejected' && s.status !== 'suggested'))

const pickLinks = computed(() =>
  (detail.value?.links ?? [])
    .filter((l) => l.status !== 'rejected')
    .map((l) => `${l.subjectName} (${l.method}${l.status === 'suggested' ? ', unconfirmed' : ''})`)
    .join(', '),
)

function addFor(e: CatalogueEntry, subject?: string) {
  const mosaic = e.fit.panels > 1
  return addLink(e.object, mosaic ? 'mosaic' : 'frame', {
    rotation: e.object.pa || undefined,
    panels: mosaic ? e.fit.panels : undefined,
    cols: mosaic ? e.fit.columns : undefined,
    rows: mosaic ? e.fit.rows : undefined,
    subject,
  })
}

const openMatches = computed(() => matches.value.filter((m) => !m.decided))
const shownMatches = computed(() => [...openMatches.value, ...matches.value.filter((m) => m.decided)])

function confTone(c: number) {
  return c >= 0.8 ? 'var(--ok)' : c >= 0.6 ? 'var(--warn)' : 'var(--bad)'
}

async function decide(m: ReviewItem, after: Decision) {
  const before: Decision = m.decided ? (m.status as Decision) : ''
  busy.value = m.subject + m.object.id
  try {
    const r = await decideMatch(m, before, after)
    notifyCommand(r)
    await Promise.all([loadMatches(), loadOverview()])
    await loadList()
  } catch (e) {
    errorToast(e, 'Could not save the match')
  } finally {
    busy.value = ''
  }
}
</script>

<template>
  <main class="page wide">
    <div class="page-head">
      <div>
        <p class="eyebrow">Discover</p>
        <h1>Catalogue completion</h1>
        <p class="lede">
          Your stacks matched to catalogue objects. Done means every filter has met its faint-SNR goal. Matches come from
          coordinates first, then names; the uncertain ones wait for you below.
        </p>
      </div>
      <div v-if="overview?.night" class="xsmall muted num" style="text-align: right">
        Tonight: dark {{ hm(overview.night.dusk) }} – {{ hm(overview.night.dawn) }} · Moon
        {{ Math.round(overview.night.moonIllumination * 100) }}% lit
      </div>
    </div>

    <p v-if="failed" class="empty">The catalogues could not be loaded: {{ failed }}</p>
    <p v-if="overview?.siteError" class="badge warn" style="align-self: flex-start">{{ overview.siteError }}</p>
    <p v-if="loading" class="empty">Loading catalogues…</p>

    <div v-if="overview" class="cats">
      <button
        v-for="c in overview.catalogues"
        :key="c.key"
        type="button"
        class="cat"
        :aria-pressed="c.key === tab"
        :style="{ borderColor: c.key === tab ? 'var(--foreground)' : 'var(--border)' }"
        @click="tab = c.key"
      >
        <span class="spread" style="width: 100%">
          <span style="font-weight: 600; font-size: 1rem">{{ c.name }}</span>
          <span class="xsmall muted">{{ c.total }} objects</span>
        </span>
        <span class="row num" style="align-items: baseline">
          <span style="font-size: 1.5rem; font-weight: 600; line-height: 1.1">{{ c.done }}</span>
          <span class="xsmall muted">done · {{ c.inProgress }} in progress · {{ c.notStarted }} not started</span>
        </span>
        <span class="bar" aria-hidden="true">
          <span :style="{ width: (c.done / c.total) * 100 + '%', background: 'var(--done)' }" />
          <span :style="{ width: (c.inProgress / c.total) * 100 + '%', background: 'var(--prog)' }" />
        </span>
        <span class="xsmall muted">Up tonight: {{ c.upTonight }}</span>
      </button>
    </div>

    <section v-if="overview" aria-labelledby="grid-h" class="card">
      <div class="spread" style="align-items: center">
        <h2 id="grid-h">{{ current?.name }}</h2>
        <div class="row small" style="gap: 0.75rem">
          <label for="f-now" class="row" style="gap: 0.375rem"><input id="f-now" v-model="tonightOnly" type="checkbox" /> Up tonight only</label>
          <label for="f-status" class="row" style="gap: 0.375rem">
            <span class="muted">Status</span>
            <select id="f-status" v-model="status" class="input" style="height: 2rem">
              <option>All</option>
              <option>Not started</option>
              <option>In progress</option>
              <option>Done</option>
            </select>
          </label>
        </div>
      </div>
      <div class="row xsmall muted" style="gap: 1rem">
        <span class="row" style="gap: 0.375rem"><span class="key" style="background: var(--done)" />Done</span>
        <span class="row" style="gap: 0.375rem"><span class="key" style="background: var(--prog-bg); border: 1px solid var(--prog)" />In progress</span>
        <span class="row" style="gap: 0.375rem"><span class="key" style="border: 1px dashed var(--muted-foreground)" />Not started</span>
        <span class="row" style="gap: 0.375rem"><span class="key" style="border: 1px solid var(--border); box-shadow: inset 0 -3px 0 var(--warn)" />Up tonight</span>
        <span>Click any object for details.</span>
      </div>

      <div v-if="pick" role="status" class="strip">
        <div style="display: flex; flex-direction: column; gap: 0.25rem; min-width: 0">
          <span>
            <strong>{{ pick.label }}</strong> {{ pick.object.name }}
            <span class="muted">
              · {{ typeLabel(pick.object.type) }} · {{ size(pick.object) }} ·
              {{ pick.fit.panels > 1 ? pick.fit.panels + ' panels' : Math.round(pick.fit.fill * 100) + '% of the frame' }}
            </span>
          </span>
          <span class="xsmall muted">
            {{ pickStatus(pick) }} · {{ window(pick) }}<template v-if="pick.tonight"> · Moon {{ Math.round(pick.tonight.moonSeparation) }}° away</template>
          </span>
          <span v-if="pickHours.length" class="row xsmall num" style="gap: 0.75rem">
            <span v-for="h in pickHours" :key="h.f" class="row" style="gap: 0.25rem"><span class="key" :style="{ background: h.colour }" />{{ h.f }} {{ hours(h.h) }}</span>
            <span class="muted">effective</span>
          </span>
          <span v-if="pickLinks" class="xsmall muted">Linked by {{ pickLinks }}</span>
        </div>
        <span class="row">
          <RouterLink v-if="pickProject" class="btn sm" :to="{ name: 'target', params: { projectId: String(pickProject.projectId) } }">Open project</RouterLink>
          <RouterLink class="btn sm primary" :to="addFor(pick)">{{ pick.status === 'not-started' ? 'Add' : 'Add again' }}</RouterLink>
          <button type="button" class="btn ghost sm" aria-label="Close" @click="pick = null">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" aria-hidden="true"><path d="M18 6 6 18M6 6l12 12" /></svg>
          </button>
        </span>
      </div>

      <p v-if="listLoading" class="empty">Loading…</p>
      <div v-else class="cells" :class="{ wide: wideCells }">
        <button
          v-for="e in entries"
          :key="e.object.id"
          type="button"
          class="cell num"
          :aria-label="`${e.label}${e.object.name ? ' ' + e.object.name : ''}: ${statusLabel[e.status]?.toLowerCase()}${e.tonight?.up ? ', up tonight' : ''}`"
          :title="label(e.object)"
          :style="cellStyle(e)"
          @click="choose(e)"
        >
          {{ cellText(e) }}
        </button>
      </div>
    </section>

    <div class="row" style="gap: 1.5rem; align-items: flex-start">
      <section aria-labelledby="match-h" class="card" style="flex: 3 1 34rem; gap: 0.75rem">
        <div class="spread">
          <h2 id="match-h">Name matches to review</h2>
          <span class="xsmall muted">{{ openMatches.length }} {{ openMatches.length === 1 ? 'match' : 'matches' }} to review</span>
        </div>
        <p class="small muted" style="margin: 0">
          Your project names against the catalogues. Confirming links the project to the object for completion; the
          scheduler's names stay as they are, so the stacker still finds its frames.
        </p>
        <p v-if="!shownMatches.length" class="empty">Nothing to review. Every project matched a catalogue object with confidence.</p>
        <ul class="list">
          <li v-for="m in shownMatches" :key="m.subject + m.object.id" class="match">
            <div style="min-width: 0">
              <div>
                <strong>{{ m.subjectName }}</strong>
                <span class="muted"> → </span>{{ label(m.object) }}
                <span class="num" :style="{ fontWeight: 600, color: confTone(m.confidence) }"> {{ Math.round(m.confidence * 100) }}%</span>
              </div>
              <div class="xsmall muted">{{ m.why }}<template v-if="m.state"> · project {{ m.state.toLowerCase() }}</template></div>
            </div>
            <div class="row" style="gap: 0.375rem">
              <template v-if="!m.decided">
                <button type="button" class="btn sm" :disabled="busy === m.subject + m.object.id" @click="decide(m, 'rejected')">Not a match</button>
                <button type="button" class="btn sm primary" :disabled="busy === m.subject + m.object.id" @click="decide(m, 'confirmed')">Confirm</button>
              </template>
              <template v-else>
                <span :class="['badge', m.status === 'confirmed' ? 'ok' : 'bad']">{{ m.status === 'confirmed' ? 'Confirmed' : 'Not a match' }}</span>
                <button type="button" class="btn link xsmall" :disabled="busy === m.subject + m.object.id" @click="decide(m, '')">Undo</button>
              </template>
            </div>
          </li>
        </ul>
      </section>

      <section aria-labelledby="up-h" class="card" style="flex: 2 1 22rem; gap: 0.75rem">
        <h2 id="up-h">Up tonight, not started · {{ current?.name }}</h2>
        <p v-if="!upNotStarted.length" class="empty">Nothing in this catalogue is both up tonight and not started.</p>
        <ul class="list">
          <li v-for="e in upNotStarted" :key="e.object.id" class="spread small upline">
            <span>
              <strong>{{ e.label }}</strong> {{ e.object.name }}
              <span class="muted">· {{ hm(e.tonight?.start) }} – {{ hm(e.tonight?.end) }}</span>
            </span>
            <RouterLink class="btn sm" :to="addFor(e)">Add</RouterLink>
          </li>
        </ul>
        <p class="xsmall muted" style="margin: 0">Add opens the wizard with the object found and the name match checked.</p>
      </section>
    </div>

    <section v-if="overview" class="xsmall muted">
      <button type="button" class="btn link xsmall" :aria-expanded="sourcesOpen" @click="sourcesOpen = !sourcesOpen">Catalogue sources and licences</button>
      <ul v-if="sourcesOpen" style="margin: 0.5rem 0 0; padding-left: 1rem">
        <li v-for="s in overview.sources" :key="s.id">
          <a :href="s.url" target="_blank" rel="noopener" class="lnk">{{ s.name }}</a> — {{ s.citation }}. {{ s.licence }}.
        </li>
      </ul>
    </section>
  </main>
</template>

<style scoped>
.cats {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 16rem), 1fr));
  gap: 1rem;
}
.cat {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  border: 1px solid var(--border);
  border-radius: 14px;
  padding: 1.125rem;
  background: var(--card);
  text-align: left;
}
.bar {
  display: flex;
  width: 100%;
  height: 0.5rem;
  border-radius: 999px;
  overflow: hidden;
  background: var(--secondary);
}
.key {
  display: inline-block;
  width: 0.875rem;
  height: 0.875rem;
  border-radius: 3px;
}
.strip {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  align-items: center;
  gap: 0.75rem;
  background: var(--secondary);
  border-radius: 0.625rem;
  padding: 0.75rem 1rem;
  font-size: 0.8125rem;
}
.cells {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(3.5rem, 1fr));
  gap: 0.25rem;
}
.cells.wide {
  grid-template-columns: repeat(auto-fill, minmax(6.5rem, 1fr));
}
.cell {
  height: 1.875rem;
  padding: 0 0.25rem;
  border-radius: 0.25rem;
  outline-offset: 1px;
  font-size: 0.6875rem;
  font-weight: 500;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
}
.match {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 0.25rem 1rem;
  align-items: center;
  padding: 0.75rem 0;
  border-top: 1px solid var(--border);
  font-size: 0.8125rem;
}
.upline {
  align-items: center;
  padding: 0.5rem 0;
  border-top: 1px solid var(--border);
}
</style>
