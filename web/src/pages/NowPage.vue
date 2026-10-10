<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { submitCommand, type CommandRecord } from '../api/commands'
import { onEvent } from '../api/events'
import { getPreview, getTonightSubs, type Preview, type TonightSub, type TonightSubs } from '../api/scheduler'
import { clock, hm, longDate, mmss, nightOf } from '../format'
import {
  capitalise,
  contribution,
  decDm,
  filterColor,
  filterName,
  isRejected,
  median,
  ms,
  pausePayload,
  priorityName,
  raHms,
  runFilters,
  runs,
  skipPayload,
  subChart,
  verdictBadge,
  type ResumeMode,
} from '../plan'
import { errorToast, exposureEnd, notifyCommand, shell } from '../shell'

const st = computed(() => shell.scheduler)
const online = computed(() => st.value.reachable === 'online')
const target = computed(() => st.value.target ?? null)
const exposure = computed(() => st.value.exposure ?? null)
const state = computed(() => (st.value.paused ? 'paused' : (st.value.state ?? '')))

const subs = ref<TonightSubs | null>(null)
const subsError = ref('')
const preview = ref<Preview | null>(null)
const previewError = ref('')

async function loadSubs() {
  try {
    subs.value = await getTonightSubs()
    subsError.value = ''
  } catch (e) {
    subsError.value = e instanceof Error ? e.message : String(e)
  }
}

async function loadPreview() {
  try {
    preview.value = await getPreview('tonight')
    previewError.value = ''
  } catch (e) {
    preview.value = null
    previewError.value = e instanceof Error ? e.message : String(e)
  }
}

let subsTimer: ReturnType<typeof setInterval> | undefined
let previewTimer: ReturnType<typeof setInterval> | undefined
let debounce: ReturnType<typeof setTimeout> | undefined
const offs: (() => void)[] = []

function soon() {
  if (debounce) clearTimeout(debounce)
  debounce = setTimeout(loadSubs, 3000)
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    skipOpen.value = false
    pauseOpen.value = false
  }
}

onMounted(() => {
  loadSubs()
  loadPreview()
  subsTimer = setInterval(loadSubs, 60000)
  previewTimer = setInterval(loadPreview, 300000)
  offs.push(onEvent('frames', soon), onEvent('preview', soon))
  window.addEventListener('keydown', onKey)
})

onUnmounted(() => {
  if (subsTimer) clearInterval(subsTimer)
  if (previewTimer) clearInterval(previewTimer)
  if (debounce) clearTimeout(debounce)
  offs.forEach((f) => f())
  window.removeEventListener('keydown', onKey)
})

const tonightLine = computed(() => {
  const tw = preview.value?.twilight
  const base = 'Night of ' + longDate(nightOf(shell.now))
  if (tw?.astronomical_dusk && tw?.astronomical_dawn) return `${base} · astronomical dark ${hm(tw.astronomical_dusk)} to ${clock(tw.astronomical_dawn)}`
  return base
})

const blockedReason = computed(() => {
  switch (st.value.reachable) {
    case 'unconfigured':
      return 'No scheduler API is configured, so there is nothing to control.'
    case 'offline':
      return 'The observatory PC is not answering.'
    case 'unknown':
      return 'Checking the observatory.'
  }
  if (st.value.web_editing === false) return 'Web editing is off on the PC.'
  return ''
})

const skipBlocked = computed(() => {
  if (blockedReason.value) return blockedReason.value
  if (!target.value || state.value !== 'imaging') return 'Nothing is imaging right now.'
  return ''
})

const pauseBlocked = computed(() => {
  if (blockedReason.value) return blockedReason.value
  if (st.value.paused || st.value.pause_requested) return 'The scheduler is already paused.'
  if (state.value !== 'imaging' && state.value !== 'waiting') return 'The scheduler is not running.'
  return ''
})

const blockedLine = computed(() => {
  if (!skipBlocked.value) return ''
  if (!pauseBlocked.value || skipBlocked.value === pauseBlocked.value) return skipBlocked.value
  return skipBlocked.value + ' ' + pauseBlocked.value
})

const waitingOf = (kind: string) => shell.waiting.filter((c) => c.kind === kind)
const pauseWaiting = computed<CommandRecord | undefined>(() => waitingOf('scheduler.pause')[0])

const pausedChip = computed(() => {
  const s = st.value
  if (s.paused) {
    let l = s.pause?.mount === 'park' ? 'Paused · mount parked' : 'Paused · mount tracking'
    if (s.pause?.resume_at) l += ' · resumes ' + hm(s.pause.resume_at)
    return l
  }
  if (s.pause_requested) return 'Pauses after this exposure'
  const w = pauseWaiting.value
  if (w) return w.status === 'queued' ? 'Pause queued offline' : 'Pause applies about ' + (exposureEnd() || 'the next plan')
  return ''
})

const skipChips = computed(() => {
  const out: { label: string; tone: string }[] = []
  for (const c of waitingOf('scheduler.skip')) {
    out.push({
      label: c.status === 'queued' ? 'Skip queued offline · sends when the PC answers' : 'Skip queued · applies about ' + (exposureEnd() || 'the next plan'),
      tone: 'warn',
    })
  }
  for (const s of st.value.skips ?? []) {
    const name = s.name || (s.scope === 'project' ? 'project ' + s.project_id : 'target ' + s.target_id)
    out.push({ label: `Skipping ${name}${s.until ? ' until ' + hm(s.until) : ''}`, tone: 'ok' })
  }
  return out
})

const systems = computed(() => {
  const s = st.value
  const conn = s.reachable === 'online' ? 'reachable' : s.reachable === 'offline' ? 'unreachable' : s.reachable === 'unconfigured' ? 'not set up' : 'checking'
  const queued = shell.waiting.filter((c) => c.status === 'queued').length
  const pend = (s.pending?.length ?? 0) + queued
  const items: { label: string; value: string; dot: string }[] = [
    { label: 'Scheduler API', value: conn, dot: s.reachable === 'online' ? 'var(--ok)' : 'var(--bad)' },
    { label: 'Link', value: s.reachable === 'online' ? (s.live ? 'live' : 'polling') : '—', dot: s.live ? 'var(--ok)' : 'var(--muted-foreground)' },
    { label: 'State', value: state.value || '—', dot: state.value === 'imaging' ? 'var(--ok)' : 'var(--muted-foreground)' },
    {
      label: 'Web editing',
      value: s.web_editing === undefined ? '—' : s.web_editing ? 'on' : 'off',
      dot: s.web_editing === false ? 'var(--bad)' : s.web_editing ? 'var(--ok)' : 'var(--muted-foreground)',
    },
    { label: 'Last plan', value: s.last_plan_at ? hm(s.last_plan_at) : '—', dot: s.last_plan_at ? 'var(--ok)' : 'var(--muted-foreground)' },
    { label: 'Pending', value: String(pend), dot: pend > 0 ? 'var(--warn)' : 'var(--ok)' },
  ]
  if (s.version) items.push({ label: 'Plugin', value: s.version, dot: 'var(--ok)' })
  return items
})

const stateBadge = computed(() => {
  switch (state.value) {
    case 'imaging':
      return { label: 'Imaging', tone: 'ok' }
    case 'waiting':
      return { label: 'Waiting', tone: 'warn' }
    case 'paused':
      return { label: 'Paused', tone: 'warn' }
    default:
      return { label: capitalise(state.value || 'unknown'), tone: '' }
  }
})

const allSubs = computed<TonightSub[]>(() => subs.value?.subs ?? [])
const targetSubs = computed(() => {
  const t = target.value
  return t ? allSubs.value.filter((s) => s.target_id === t.target_id) : []
})

const expProgress = computed(() => {
  const e = exposure.value
  const start = ms(e?.started_at)
  const end = ms(e?.ends_at)
  if (!e || isNaN(start) || isNaN(end) || end <= start) return null
  const len = (end - start) / 1000
  const elapsed = Math.min(len, Math.max(0, (shell.now - start) / 1000))
  return { pct: Math.round((elapsed / len) * 100), elapsed, len, end }
})

const facts = computed(() => {
  const t = target.value
  const e = exposure.value
  const out: { label: string; value: string; note: string }[] = []
  if (e) {
    out.push({ label: 'Filter', value: filterName(e.filter), note: e.number ? 'exposure ' + e.number : '' })
    out.push({ label: 'Exposure', value: Math.round(e.seconds) + ' s', note: e.started_at ? 'started ' + hm(e.started_at) : '' })
  }
  if (t) {
    out.push({ label: 'Minimum time', value: t.minimum_time_end ? hm(t.minimum_time_end) : '—', note: 'window ends' })
    out.push({ label: 'Hard stop', value: t.hard_stop ? hm(t.hard_stop) : '—', note: 'must end by' })
    if (t.picked_at) {
      const total = st.value.score_total
      out.push({ label: 'Picked', value: hm(t.picked_at), note: total !== null && total !== undefined ? 'score ' + total.toFixed(2) : '' })
    }
  }
  return out
})

const rules = computed(() => {
  const out = (st.value.scores ?? []).map((s) => ({ name: s.rule, score: contribution(s), title: `weight ${s.weight} × score ${s.score}` }))
  const total = st.value.score_total
  if (total !== null && total !== undefined) out.push({ name: 'Total now', score: total, title: '' })
  return out
})

const latest = computed(() => subs.value?.latest ?? null)
const latestTargetSubs = computed(() => {
  const l = latest.value
  return l ? allSubs.value.filter((s) => s.target_id === l.target_id) : []
})
const latestHours = computed(() => latestTargetSubs.value.reduce((a, s) => a + (s.exposure ?? 0), 0) / 3600)
const latestVerdict = computed(() => verdictBadge(latest.value?.verdict))

const chartRange = computed<[number, number]>(() => {
  const list = allSubs.value
  const first = list.length ? ms(list[0].time) : NaN
  const since = ms(subs.value?.since)
  const t0 = isNaN(first) ? (isNaN(since) ? shell.now - 6 * 3600 * 1000 : since) : first - 10 * 60 * 1000
  const lastT = list.length ? ms(list[list.length - 1].time) : NaN
  const t1 = Math.max(shell.now, isNaN(lastT) ? 0 : lastT + 5 * 60 * 1000, t0 + 3600 * 1000)
  return [t0, t1]
})

const hfrBox = { x0: 56, x1: 1180, yTop: 20, yBottom: 190 }
const rmsBox = { x0: 56, x1: 1180, yTop: 8, yBottom: 70 }
const curId = computed(() => target.value?.target_id ?? latest.value?.target_id)
const hfr = computed(() => subChart(allSubs.value, (s) => s.hfr, curId.value, chartRange.value[0], chartRange.value[1], hfrBox, { minSpan: 1 }))
const rms = computed(() => subChart(allSubs.value, (s) => s.guiding_rms, curId.value, chartRange.value[0], chartRange.value[1], rmsBox, { zero: true, minSpan: 1, unit: '″' }))
const hasHfr = computed(() => allSubs.value.some((s) => s.hfr !== undefined))
const hasRms = computed(() => allSubs.value.some((s) => s.guiding_rms !== undefined))

const hfrStats = computed(() => {
  const list = allSubs.value
  const cur = curId.value
  const curName = list.find((s) => s.target_id === cur)?.target ?? target.value?.target_name ?? ''
  const hfrsOf = (id: number | undefined) => list.filter((s) => s.target_id === id && !isRejected(s) && s.hfr !== undefined).map((s) => s.hfr as number)
  const prev = [...list].reverse().find((s) => s.target_id !== cur)
  const last = [...list].reverse().find((s) => s.guiding_rms !== undefined)
  return {
    curName,
    curMed: median(hfrsOf(cur)),
    prevName: prev?.target ?? '',
    prevMed: prev ? median(hfrsOf(prev.target_id)) : null,
    last,
    rejected: list.filter(isRejected).length,
    total: list.length,
  }
})

const nowX = computed(() => {
  const [t0, t1] = chartRange.value
  return hfrBox.x0 + ((Math.min(shell.now, t1) - t0) / (t1 - t0)) * (hfrBox.x1 - hfrBox.x0)
})

const upcoming = computed(() => {
  const now = shell.now
  const cur = target.value?.target_id
  return runs(preview.value?.blocks).filter((r) => r.end > now && !(r.start <= now && !r.wait && r.target_id === cur))
})

const nextUp = computed(() =>
  upcoming.value.slice(0, 3).map((r) => {
    const names = [r.project_name, r.target_name].filter((x): x is string => !!x)
    const total = r.pick?.total
    return {
      start: hm(r.start),
      end: hm(r.end),
      name: r.wait ? 'Wait' : [...new Set(names)].join(' · '),
      filters: r.wait ? '' : runFilters(r),
      why: r.wait ? 'Nothing is up yet' : [total !== null && total !== undefined ? 'score ' + total.toFixed(2) : '', r.reason ? 'ends: ' + r.reason : ''].filter(Boolean).join(' · '),
    }
  }),
)

const skipOpen = ref(false)
const skipFor = ref<'60' | 'night'>('60')
const skipWhole = ref(false)
const pauseOpen = ref(false)
const pauseMount = ref<'track' | 'park'>('track')
const resumeMode = ref<ResumeMode>('manual')
const resumeAt = ref('03:00')
const busy = ref(false)

const endHm = computed(() => exposureEnd() || 'the end of this exposure')
const nextTarget = computed(() => {
  const t = target.value
  return t ? upcoming.value.find((x) => !x.wait && x.target_id !== t.target_id) : undefined
})
const nextProject = computed(() => {
  const t = target.value
  return t ? upcoming.value.find((x) => !x.wait && x.project_id !== t.project_id) : undefined
})
const nextTargetScore = computed(() => {
  const v = nextTarget.value?.pick?.total
  return v !== null && v !== undefined ? v.toFixed(2) : ''
})

function openSkip() {
  skipFor.value = '60'
  skipWhole.value = false
  skipOpen.value = true
}

function openPause() {
  pauseMount.value = 'track'
  resumeMode.value = 'manual'
  pauseOpen.value = true
}

async function send(kind: string, payload: unknown) {
  busy.value = true
  try {
    notifyCommand(await submitCommand(kind, payload))
  } catch (e) {
    errorToast(e)
  } finally {
    busy.value = false
  }
}

async function confirmSkip() {
  const t = target.value
  skipOpen.value = false
  if (!t) return
  await send('scheduler.skip', skipPayload(t, skipWhole.value, skipFor.value === '60' ? 60 : 0))
}

async function confirmPause() {
  pauseOpen.value = false
  await send('scheduler.pause', pausePayload(pauseMount.value, resumeMode.value, resumeAt.value))
}

async function resume() {
  await send('scheduler.resume', {})
}

const fmt = (v: number | null | undefined, d = 2) => (v === null || v === undefined ? '—' : v.toFixed(d))
const cross = (x: number, y: number, r: number) => `M${x - r} ${y - r} L${x + r} ${y + r} M${x + r} ${y - r} L${x - r} ${y + r}`
</script>

<template>
  <main id="now" class="page wide">
    <div class="page-head">
      <div>
        <p class="eyebrow-line">{{ tonightLine }}</p>
        <h1>Now <span class="clock num">{{ clock(shell.now) }}</span></h1>
        <p class="lede">What the observatory is doing right now. Edits and skips apply when the current exposure finishes, then the scheduler re-plans.</p>
      </div>
      <div class="row">
        <span v-if="pausedChip" role="status" class="badge warn">
          {{ pausedChip }} ·
          <button type="button" class="inline-link" :disabled="busy" @click="resume">resume</button>
        </span>
        <span v-for="c in skipChips" :key="c.label" role="status" class="badge" :class="c.tone">{{ c.label }}</span>
        <button
          type="button"
          class="btn pause-btn"
          aria-haspopup="dialog"
          :aria-expanded="pauseOpen ? 'true' : 'false'"
          :disabled="!!pauseBlocked || busy"
          :title="pauseBlocked"
          @click="openPause"
        >
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="M8 5v14M16 5v14" /></svg>
          Pause scheduler
        </button>
        <button
          type="button"
          class="btn danger skip-btn"
          aria-haspopup="dialog"
          :aria-expanded="skipOpen ? 'true' : 'false'"
          :disabled="!!skipBlocked || busy"
          :title="skipBlocked"
          @click="openSkip"
        >
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 4l10 8-10 8V4zM19 5v14" /></svg>
          Skip this target
        </button>
      </div>
    </div>
    <p v-if="blockedLine" class="muted small blocked">{{ blockedLine }}</p>

    <section aria-label="System status" class="row">
      <span v-for="s in systems" :key="s.label" class="pill">
        <svg width="7" height="7" viewBox="0 0 8 8" aria-hidden="true"><circle cx="4" cy="4" r="4" :fill="s.dot" /></svg>
        <span class="muted">{{ s.label }}</span>
        <span class="num" style="font-weight: 500">{{ s.value }}</span>
      </span>
    </section>

    <div class="cards">
      <section aria-labelledby="current-h" class="card current">
        <template v-if="target">
          <div class="spread top">
            <div>
              <div class="row">
                <span class="badge" :class="stateBadge.tone">
                  <svg width="7" height="7" viewBox="0 0 8 8" aria-hidden="true"><circle cx="4" cy="4" r="4" fill="currentColor" /></svg>
                  {{ stateBadge.label }}
                </span>
                <span v-if="!online" class="badge warn">Last known{{ st.last_answer ? ' · ' + hm(st.last_answer) : '' }}</span>
                <span v-if="target.priority !== undefined && target.priority !== null" class="badge">{{ priorityName(target.priority) }} priority</span>
                <span v-if="target.is_mosaic" class="badge">Mosaic</span>
              </div>
              <h2 id="current-h" class="target-h">
                <RouterLink class="lnk" :to="{ name: 'target', params: { projectId: String(target.project_id) } }">{{ target.project_name }}</RouterLink>
                <span class="muted" style="font-weight: 400"> / </span>{{ target.target_name }}
              </h2>
              <p class="muted small coords num">
                <template v-if="target.ra_hours !== undefined && target.ra_hours !== null">RA {{ raHms(target.ra_hours) }} · </template>
                <template v-if="target.dec_degrees !== undefined && target.dec_degrees !== null">Dec {{ decDm(target.dec_degrees) }} · </template>
                <template v-if="target.rotation !== undefined && target.rotation !== null">rotation {{ target.rotation.toFixed(0) }}°</template>
              </p>
            </div>
            <div style="text-align: right">
              <div class="xsmall muted">Subs tonight</div>
              <div class="big num">{{ targetSubs.length }}</div>
              <div class="xsmall muted">{{ allSubs.length }} in all tonight</div>
            </div>
          </div>

          <div v-if="facts.length" class="facts">
            <div v-for="f in facts" :key="f.label" class="fact">
              <div class="xsmall muted">{{ f.label }}</div>
              <div class="num" style="font-weight: 600">{{ f.value }}</div>
              <div class="xsmall muted">{{ f.note }}</div>
            </div>
          </div>

          <div v-if="exposure">
            <div class="spread small" style="margin-bottom: 0.375rem">
              <span>
                <span class="swatch" :style="{ background: filterColor(exposure.filter) }"></span>
                {{ exposure.number ? 'Exposure ' + exposure.number + ' · ' : '' }}{{ filterName(exposure.filter) }} {{ Math.round(exposure.seconds) }} s
              </span>
              <span v-if="expProgress" class="num muted">{{ mmss(expProgress.elapsed) }} of {{ mmss(expProgress.len) }} · finishes {{ hm(expProgress.end) }}</span>
            </div>
            <div role="progressbar" aria-label="Exposure progress" :aria-valuenow="expProgress?.pct ?? 0" aria-valuemin="0" aria-valuemax="100" class="bar">
              <div :style="{ width: (expProgress?.pct ?? 0) + '%' }"></div>
            </div>
            <div class="spread xsmall muted" style="margin-top: 0.5rem">
              <span>{{ target.minimum_time_end ? 'Minimum-time window ends ' + hm(target.minimum_time_end) : 'No minimum-time window' }}</span>
              <span v-if="target.hard_stop">Hard stop {{ hm(target.hard_stop) }}</span>
            </div>
          </div>
          <p v-else-if="state === 'paused'" class="muted small" style="margin: 0">Paused · no exposure running. Resume to start the next one.</p>

          <div class="why">
            <div class="spread">
              <h3>Why this target</h3>
              <RouterLink :to="{ name: 'tonight' }" class="lnk small muted">Open tonight's plan</RouterLink>
            </div>
            <p class="small muted" style="margin: 0.25rem 0 0.75rem">
              <template v-if="target.picked_at">Picked at {{ hm(target.picked_at) }}. </template>Score is the sum of rule weight × rule score.
            </p>
            <div v-if="rules.length" class="rules">
              <div v-for="r in rules" :key="r.name" class="rule small" :title="r.title">
                <span>{{ r.name }}</span><span class="num" style="font-weight: 600">{{ r.score.toFixed(2) }}</span>
              </div>
            </div>
            <p v-else class="empty" style="padding: 0">The scheduler did not send its scores.</p>
          </div>
        </template>
        <template v-else>
          <h2 id="current-h">Current target</h2>
          <p v-if="st.reachable === 'unconfigured'" class="empty">
            No scheduler API is configured, so the current target is unknown. Edits are still recorded and wait in History.
          </p>
          <p v-else-if="st.reachable === 'offline'" class="empty">
            The observatory PC is not answering{{ st.since ? ' since ' + hm(st.since) : '' }}. Edits wait in order and go out when it does.
          </p>
          <p v-else-if="st.reachable === 'unknown'" class="empty">Checking the observatory.</p>
          <p v-else-if="state === 'waiting'" class="empty">
            Waiting{{ st.wait?.target_name ? ' for ' + st.wait.target_name : '' }}{{ st.wait?.until ? ' until ' + hm(st.wait.until) : '' }}.
          </p>
          <p v-else-if="state === 'paused'" class="empty">The scheduler is paused.</p>
          <p v-else class="empty">The scheduler is {{ state || 'not running' }}. Nothing is being imaged.</p>
        </template>
      </section>

      <section aria-labelledby="sub-h" class="card latest">
        <div class="spread">
          <h2 id="sub-h">Latest sub</h2>
          <span v-if="latest" class="xsmall muted">
            saved {{ hm(latest.time) }}<template v-if="latest.processed_at"> · graded {{ hm(latest.processed_at) }}</template><template v-if="latest.preview_url"> · stretched preview</template>
          </span>
        </div>
        <template v-if="latest">
          <img v-if="latest.preview_url" :src="latest.preview_url" :alt="'Stretched preview of the latest ' + filterName(latest.filter) + ' sub of ' + latest.target" class="sky" />
          <svg v-else viewBox="0 0 300 200" role="img" aria-label="No preview of the latest sub yet" class="sky">
            <rect x="0" y="0" width="300" height="200" fill="var(--sky)" />
            <text x="150" y="104" text-anchor="middle" font-size="11" fill="var(--muted-foreground)">No preview yet</text>
          </svg>
          <div class="spread" style="align-items: flex-start; gap: 1rem 1.5rem">
            <div style="display: flex; flex-direction: column; gap: 0.25rem; min-width: 0">
              <div style="font-weight: 600; font-size: 0.9375rem">{{ latest.target }} <span class="muted" style="font-weight: 400">· {{ latest.project }}</span></div>
              <div class="small muted num">
                {{ filterName(latest.filter) }}<template v-if="latest.exposure"> · {{ Math.round(latest.exposure) }} s</template><template v-if="latest.gain !== undefined"> · gain {{ latest.gain }}</template><template v-if="latest.ccd_temp !== undefined"> · {{ latest.ccd_temp.toFixed(1) }} °C</template><template v-if="latest.hfr !== undefined"> · HFR {{ latest.hfr.toFixed(2) }}</template>
              </div>
              <RouterLink :to="{ name: 'target', params: { projectId: String(latest.project_id) } }" class="small underline">This target's goal and subs</RouterLink>
            </div>
            <div class="row num" style="gap: 1.25rem; align-items: flex-start">
              <div>
                <div class="xsmall muted">Stacker verdict</div>
                <span class="badge" :class="latestVerdict.tone" style="margin-top: 0.125rem">{{ latestVerdict.label }}</span>
              </div>
              <div><div class="xsmall muted">Score</div><div class="stat">{{ fmt(latest.score) }}</div></div>
              <div><div class="xsmall muted">Weight</div><div class="stat">{{ latest.weight !== undefined ? Math.round(latest.weight) + ' s' : '—' }}</div></div>
              <div><div class="xsmall muted">Target tonight</div><div class="stat">{{ latestTargetSubs.length }} · {{ latestHours.toFixed(2) }} h</div></div>
            </div>
          </div>
        </template>
        <p v-else-if="subsError" class="empty">Could not read tonight's subs: {{ subsError }}</p>
        <p v-else-if="subs" class="empty">No subs yet tonight.</p>
        <p v-else class="empty">Loading.</p>
      </section>
    </div>

    <section aria-labelledby="hfr-h" class="card">
      <div class="spread" style="align-items: flex-end; gap: 1rem">
        <div>
          <h2 id="hfr-h">Star size and guiding tonight</h2>
          <p class="small muted" style="margin: 0.125rem 0 0">HFR of every sub since dusk. The line restarts at each target change; the current target is drawn bold.</p>
        </div>
        <div class="row num" style="gap: 1.5rem; align-items: flex-start">
          <div v-if="hfrStats.curName">
            <div class="xsmall muted">{{ hfrStats.curName }} median HFR</div>
            <div class="stat-lg">{{ hfrStats.curMed !== null ? hfrStats.curMed.toFixed(2) + ' px' : '—' }}</div>
          </div>
          <div v-if="hfrStats.prevName">
            <div class="xsmall muted">{{ hfrStats.prevName }} median</div>
            <div class="stat-lg">{{ hfrStats.prevMed !== null ? hfrStats.prevMed.toFixed(2) + ' px' : '—' }}</div>
          </div>
          <div>
            <div class="xsmall muted">Guiding, last sub</div>
            <div class="stat-lg">
              {{ hfrStats.last?.guiding_rms !== undefined ? fmt(hfrStats.last.guiding_rms) + '″' : '—' }}
              <span v-if="hfrStats.last && (hfrStats.last.guiding_rms_ra !== undefined || hfrStats.last.guiding_rms_dec !== undefined)" class="small muted" style="font-weight: 400">
                RA {{ fmt(hfrStats.last.guiding_rms_ra) }} · Dec {{ fmt(hfrStats.last.guiding_rms_dec) }}
              </span>
            </div>
          </div>
          <div>
            <div class="xsmall muted">Rejected tonight</div>
            <div class="stat-lg">{{ hfrStats.rejected }} of {{ hfrStats.total }}</div>
          </div>
        </div>
      </div>
      <p v-if="subsError" class="empty">Could not read tonight's subs: {{ subsError }}</p>
      <p v-else-if="subs && !allSubs.length" class="empty">No subs yet tonight. The charts fill in as the scheduler saves them.</p>
      <div v-else-if="subs" class="scroll-x">
        <div style="min-width: 44rem; display: flex; flex-direction: column; gap: 0.25rem">
          <div class="xsmall muted">HFR, px</div>
          <svg
            v-if="hasHfr"
            viewBox="0 0 1200 216"
            role="img"
            :aria-label="'HFR per sub since ' + hm(chartRange[0]) + ', ' + allSubs.length + ' subs, ' + hfrStats.rejected + ' rejected'"
            style="width: 100%; display: block"
          >
            <template v-for="(tk, i) in hfr.yTicks" :key="'y' + i">
              <line :x1="hfrBox.x0" :y1="tk.pos" :x2="hfrBox.x1" :y2="tk.pos" stroke="var(--border)" stroke-width="1" :stroke-dasharray="i === 0 ? undefined : '3 4'" />
              <text :x="hfrBox.x0 - 6" :y="tk.pos + 4" text-anchor="end" font-size="11" fill="var(--muted-foreground)">{{ tk.label }}</text>
            </template>
            <text v-for="tk in hfr.xTicks" :key="'x' + tk.pos" :x="tk.pos" y="210" text-anchor="middle" font-size="11" fill="var(--muted-foreground)">{{ tk.label }}</text>
            <text :x="hfrBox.x1" y="210" text-anchor="end" font-size="11" fill="var(--foreground)">{{ hm(shell.now) }} now</text>
            <line v-if="nowX < hfrBox.x1 - 2" :x1="nowX" y1="12" :x2="nowX" y2="192" stroke="var(--foreground)" stroke-width="1" stroke-dasharray="2 3" opacity="0.6" />
            <text v-if="hfr.firstLabel" :x="hfrBox.x0 + 8" y="14" font-size="11" fill="var(--muted-foreground)">{{ hfr.firstLabel }}</text>
            <template v-for="(c, i) in hfr.changes" :key="'c' + i">
              <line :x1="c.x" y1="12" :x2="c.x" y2="192" stroke="var(--foreground)" stroke-width="1" stroke-dasharray="2 3" />
              <text v-if="i === hfr.changes.length - 1" :x="c.x - 6" y="14" text-anchor="end" font-size="11" font-weight="600" fill="var(--foreground)">{{ c.label }} →</text>
            </template>
            <line
              v-for="(m, i) in hfr.medians"
              :key="'m' + i"
              :x1="m.x1"
              :y1="m.y"
              :x2="m.x2"
              :y2="m.y"
              :stroke="m.current ? 'var(--ok)' : 'var(--muted-foreground)'"
              :stroke-width="m.current ? 1.5 : 1"
              :stroke-dasharray="m.current ? '4 3' : '1 3'"
            />
            <path
              v-for="(p, i) in hfr.paths"
              :key="'p' + i"
              :d="p.d"
              fill="none"
              :stroke="p.current ? 'var(--foreground)' : 'var(--muted-foreground)'"
              :stroke-width="p.current ? 2.25 : 1.25"
              :opacity="p.current ? 1 : 0.8"
            />
            <template v-for="(pt, i) in hfr.points" :key="'pt' + i">
              <path v-if="pt.rejected" :d="cross(pt.x, pt.y, 4)" stroke="var(--bad)" stroke-width="2" />
              <circle v-else :cx="pt.x" :cy="pt.y" :r="pt.current ? 4 : 2.5" :fill="pt.current ? 'var(--foreground)' : 'var(--muted-foreground)'" />
            </template>
          </svg>
          <p v-else class="empty">The scheduler recorded no HFR for tonight's subs.</p>
          <div class="xsmall muted">Guiding total RMS per sub, same time axis</div>
          <svg v-if="hasRms" viewBox="0 0 1200 76" role="img" aria-label="Guiding total RMS per sub in arcseconds" style="width: 100%; display: block">
            <template v-for="(tk, i) in rms.yTicks" :key="'ry' + i">
              <line :x1="rmsBox.x0" :y1="tk.pos" :x2="rmsBox.x1" :y2="tk.pos" stroke="var(--border)" :stroke-dasharray="i === 0 ? undefined : '3 4'" />
              <text :x="rmsBox.x0 - 6" :y="tk.pos + 4" text-anchor="end" font-size="11" fill="var(--muted-foreground)">{{ tk.label }}</text>
            </template>
            <line v-for="(c, i) in rms.changes" :key="'rc' + i" :x1="c.x" y1="4" :x2="c.x" y2="72" stroke="var(--foreground)" stroke-width="1" stroke-dasharray="2 3" />
            <path v-for="(p, i) in rms.paths" :key="'rp' + i" :d="p.d" fill="none" stroke="var(--blue)" :stroke-width="p.current ? 2.25 : 1.75" />
            <template v-for="(pt, i) in rms.points" :key="'rpt' + i">
              <path v-if="pt.rejected" :d="cross(pt.x, pt.y, 3)" stroke="var(--bad)" stroke-width="1.5" />
            </template>
          </svg>
          <p v-else class="empty">The scheduler recorded no guiding RMS for tonight's subs.</p>
        </div>
      </div>
      <p v-else class="empty">Loading.</p>
      <div class="row xsmall muted" style="gap: 1rem">
        <span class="legend"><svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true"><circle cx="7" cy="7" r="4" fill="var(--foreground)" /></svg>Current target</span>
        <span class="legend"><svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true"><circle cx="7" cy="7" r="2.5" fill="var(--muted-foreground)" /></svg>Earlier target</span>
        <span class="legend"><svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true"><path d="M3 3 L11 11 M11 3 L3 11" stroke="var(--bad)" stroke-width="2" /></svg>Rejected by the stacker or the grader</span>
        <span class="legend"><svg width="18" height="14" viewBox="0 0 18 14" aria-hidden="true"><path d="M1 7h16" stroke="var(--ok)" stroke-width="1.5" stroke-dasharray="4 3" /></svg>Median for this target</span>
      </div>
    </section>

    <div class="lower">
      <section aria-labelledby="next-h" class="card" style="gap: 0.75rem">
        <div class="spread">
          <h2 id="next-h">Next up</h2>
          <RouterLink :to="{ name: 'tonight' }" class="lnk small muted">{{ preview?.generated_at ? 'Simulated ' + hm(preview.generated_at) : "Tonight's plan" }}</RouterLink>
        </div>
        <ol v-if="nextUp.length" class="next">
          <li v-for="(n, i) in nextUp" :key="i">
            <div class="num">
              <div style="font-weight: 600">{{ n.start }}</div>
              <div class="xsmall muted">to {{ n.end }}</div>
            </div>
            <div>
              <div style="font-weight: 600">{{ n.name }}</div>
              <div v-if="n.filters" class="small muted">{{ n.filters }}</div>
              <div v-if="n.why" class="xsmall muted">{{ n.why }}</div>
            </div>
          </li>
        </ol>
        <p v-else-if="previewError" class="empty">The plan is not available: {{ previewError }}</p>
        <p v-else-if="preview" class="empty">Nothing else is planned tonight.</p>
        <p v-else class="empty">Loading.</p>
      </section>
    </div>

    <div v-if="skipOpen && target" class="overlay">
      <div class="scrim" aria-hidden="true" @click="skipOpen = false"></div>
      <div role="dialog" aria-modal="true" aria-labelledby="skip-h" aria-describedby="skip-d" class="dialog">
        <div class="row" style="gap: 0.625rem">
          <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="var(--destructive-foreground)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 4l10 8-10 8V4zM19 5v14" /></svg>
          <h2 id="skip-h">Skip {{ target.target_name }}?</h2>
        </div>
        <p id="skip-d" style="font-size: 0.9375rem; font-weight: 500">This finishes the current exposure, then re-plans. Applies about {{ endHm }}.</p>
        <p class="small muted">
          <template v-if="exposure">The {{ filterName(exposure.filter) }} sub in progress is kept. </template>
          At about {{ endHm }} the scheduler drops the {{ target.is_mosaic ? 'panel' : 'target' }} and picks again.
          <template v-if="nextTarget">
            If tonight's plan holds, that is <strong class="fg">{{ nextTarget.target_name }}</strong><template v-if="nextTargetScore"> (score {{ nextTargetScore }})</template>.
          </template>
          <template v-if="target.is_mosaic && nextProject && nextProject !== nextTarget"> Skip the whole mosaic to move on to {{ nextProject.target_name }}.</template>
        </p>
        <fieldset class="plain">
          <legend>Keep it skipped</legend>
          <label class="opt"><input v-model="skipFor" type="radio" name="skip-for" value="60" /> For 60 minutes</label>
          <label class="opt"><input v-model="skipFor" type="radio" name="skip-for" value="night" /> For the rest of tonight</label>
          <label class="opt"><input v-model="skipWhole" type="checkbox" /> Skip the whole {{ target.project_name }} {{ target.is_mosaic ? 'mosaic' : 'project' }}</label>
        </fieldset>
        <div class="actions">
          <button type="button" class="btn" @click="skipOpen = false">Cancel</button>
          <button type="button" class="btn danger" :disabled="busy" @click="confirmSkip">Skip after this exposure</button>
        </div>
        <p class="xsmall muted foot">Abort now (discard the partial sub) is a separate action under Equipment.</p>
      </div>
    </div>

    <div v-if="pauseOpen" class="overlay">
      <div class="scrim" aria-hidden="true" @click="pauseOpen = false"></div>
      <div role="dialog" aria-modal="true" aria-labelledby="pause-h" aria-describedby="pause-d" class="dialog">
        <div class="row" style="gap: 0.625rem">
          <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="var(--warn)" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="M8 5v14M16 5v14" /></svg>
          <h2 id="pause-h">Pause the scheduler?</h2>
        </div>
        <p id="pause-d" style="font-size: 0.9375rem; font-weight: 500">This finishes the current exposure, then stops picking targets.</p>
        <p v-if="exposure && target" class="small muted">The {{ filterName(exposure.filter) }} sub on {{ target.target_name }} is kept, about {{ endHm }}. Guiding then stops.</p>
        <fieldset class="plain">
          <legend>While paused, the mount</legend>
          <label class="opt"><input v-model="pauseMount" type="radio" name="pause-mount" value="track" /> Keeps tracking</label>
          <label class="opt"><input v-model="pauseMount" type="radio" name="pause-mount" value="park" /> Parks</label>
        </fieldset>
        <fieldset class="plain">
          <legend>Resume</legend>
          <label class="opt"><input v-model="resumeMode" type="radio" name="pause-until" value="manual" /> When I resume</label>
          <label class="opt"><input v-model="resumeMode" type="radio" name="pause-until" value="after" /> After 30 minutes</label>
          <div class="row">
            <label class="opt"><input v-model="resumeMode" type="radio" name="pause-until" value="at" /> At a set time</label>
            <label for="pause-at" class="sr-only">Resume time</label>
            <input id="pause-at" v-model="resumeAt" type="time" class="input time" @focus="resumeMode = 'at'" />
          </div>
        </fieldset>
        <div class="actions">
          <button type="button" class="btn" @click="pauseOpen = false">Cancel</button>
          <button type="button" class="btn primary" :disabled="busy || (resumeMode === 'at' && !resumeAt)" @click="confirmPause">Pause after this exposure</button>
        </div>
        <p class="xsmall muted foot">Dawn flats and the end-of-night shutdown still run, even while paused.</p>
      </div>
    </div>
  </main>
</template>

<style scoped>
.eyebrow-line {
  margin: 0;
  font-size: 0.875rem;
  color: var(--muted-foreground);
}
.clock {
  font-weight: 400;
  color: var(--muted-foreground);
  font-size: 1rem;
}
.blocked {
  margin: -1rem 0 0;
  text-align: right;
}
.pause-btn {
  background: oklch(0.274 0.006 286.033 / 0.3);
}
.skip-btn {
  height: 2.5rem;
  padding: 0 1.25rem;
  font-size: 0.9375rem;
}
.inline-link {
  border: 0;
  background: transparent;
  padding: 0;
  color: inherit;
  font-size: inherit;
  font-weight: 600;
  text-decoration: underline;
  text-underline-offset: 2px;
}
.underline {
  text-decoration: underline;
  text-underline-offset: 3px;
}
.fg {
  color: var(--foreground);
  font-weight: 600;
}
.cards {
  display: flex;
  flex-wrap: wrap;
  gap: 1.5rem;
  align-items: flex-start;
}
.current {
  flex: 1 1 26rem;
  gap: 1.25rem;
}
.latest {
  flex: 1.45 1 34rem;
}
.top {
  align-items: flex-start;
  gap: 0.75rem;
}
.card h2.target-h {
  margin: 0.5rem 0 0;
  font-size: 1.375rem;
  line-height: 1.3;
}
.coords {
  margin: 0.125rem 0 0;
}
.big {
  font-size: 1.75rem;
  font-weight: 600;
  line-height: 1.1;
}
.facts {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 9rem), 1fr));
  gap: 0.75rem;
}
.fact {
  border: 1px solid var(--border);
  border-radius: 0.5rem;
  padding: 0.625rem 0.75rem;
}
.swatch {
  display: inline-block;
  width: 0.625rem;
  height: 0.625rem;
  border-radius: 2px;
  margin-right: 0.375rem;
}
.bar {
  height: 0.5rem;
  border-radius: 999px;
  background: var(--secondary);
  overflow: hidden;
}
.bar > div {
  height: 100%;
  background: var(--primary);
  transition: width 0.9s linear;
}
.why {
  border-top: 1px solid var(--border);
  padding-top: 1rem;
}
.why h3 {
  margin: 0;
  font-size: 0.9375rem;
  font-weight: 600;
}
.rules {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 10rem), 1fr));
  gap: 0.5rem;
}
.rule {
  display: flex;
  justify-content: space-between;
  gap: 0.5rem;
  background: var(--secondary);
  border-radius: 0.375rem;
  padding: 0.375rem 0.625rem;
}
.sky {
  display: block;
  width: 100%;
  aspect-ratio: 3 / 2;
  height: auto;
  border-radius: 0.5rem;
  background: var(--sky);
  object-fit: contain;
}
.stat {
  font-size: 1.25rem;
  font-weight: 600;
  line-height: 1.3;
}
.stat-lg {
  font-size: 1.375rem;
  font-weight: 600;
}
.legend {
  display: inline-flex;
  align-items: center;
  gap: 0.375rem;
}
.lower {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 22rem), 1fr));
  gap: 1.5rem;
  align-items: start;
}
.next {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
}
.next li {
  display: grid;
  grid-template-columns: 4.25rem 1fr;
  gap: 0.75rem;
  padding: 0.75rem 0;
  border-top: 1px solid var(--border);
}
fieldset.plain {
  border: 0;
  padding: 0;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}
fieldset.plain legend {
  font-size: 0.8125rem;
  font-weight: 500;
  color: var(--foreground);
  margin-bottom: 0.375rem;
  padding: 0;
}
.opt {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 0.875rem;
}
.input.time {
  height: 2rem;
  font-size: 0.8125rem;
}
.foot {
  margin: 0;
  border-top: 1px solid var(--border);
  padding-top: 0.75rem;
}
</style>
