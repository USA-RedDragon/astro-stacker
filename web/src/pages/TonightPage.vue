<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { editEntity } from '../api/commands'
import { getMoon, getPreview, getProjects, postPreview, type MoonNight, type Night, type OverrideField, type Preview, type SchedProject } from '../api/scheduler'
import { clock, hm, longDate, nightOf } from '../format'
import {
  FIELD_LABELS,
  addChange,
  duration,
  editsFor,
  fieldValue,
  filterColor,
  hourTicks,
  ms,
  nightRange,
  overrides,
  pickTable,
  planDiff,
  planSummary,
  runs,
  scale,
  twilightBands,
  valueChoices,
  valueLabel,
  type Run,
  type WhatIfChange,
} from '../plan'
import { errorToast, notifyCommand, shell, showToast } from '../shell'
import { moonLine, moonPhaseText, mosaicBalance, nightSpan, noMoonAvoidance } from '../nowtonight'

const night = ref<Night>('tonight')
const preview = ref<Preview | null>(null)
const error = ref('')
const loading = ref(false)

async function load(fresh = false) {
  loading.value = true
  try {
    preview.value = await getPreview(night.value, fresh)
    error.value = ''
    loadMoon()
    if (fresh) showToast({ text: 'Re-simulated at ' + hm(Date.now()), sub: "The plan now reflects the scheduler's current state." })
  } catch (e) {
    preview.value = null
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

const moonNight = ref<MoonNight | null>(null)
const moonError = ref('')

async function loadMoon() {
  const r = nightRange(preview.value)
  if (!r) return
  try {
    moonNight.value = await getMoon(new Date(r[0]).toISOString(), new Date(r[1]).toISOString())
    moonError.value = ''
  } catch (e) {
    moonNight.value = null
    moonError.value = e instanceof Error ? e.message : String(e)
  }
}

const projects = ref<SchedProject[]>([])
const projectsError = ref('')

async function loadProjects() {
  try {
    projects.value = await getProjects()
    projectsError.value = ''
  } catch (e) {
    projects.value = []
    projectsError.value = e instanceof Error ? e.message : String(e)
  }
}

onMounted(() => {
  load()
  loadProjects()
})

watch(night, () => {
  load()
  if (changes.value.length) runWhatIf()
})

const nightDate = computed(() => {
  const base = nightOf(shell.now)
  return night.value === 'tonight' ? base : new Date(base.getTime() + 24 * 3600 * 1000)
})

const nightLabel = computed(() => {
  const d = 'Night of ' + longDate(nightDate.value)
  if (preview.value?.generated_at) return `${d} · simulated ${clock(preview.value.generated_at)} on the scheduler's own planner`
  return d
})

const summary = computed(() => planSummary(preview.value))
const tiles = computed(() => {
  const s = summary.value
  return [
    {
      label: 'Astronomical dark',
      value: !isNaN(s.darkStart) && !isNaN(s.darkEnd) ? `${hm(s.darkStart)} – ${hm(s.darkEnd)}` : 'None',
      note: isNaN(s.darkSeconds) ? 'the sun never gets 18° down' : duration(s.darkSeconds),
    },
    { label: 'Planned imaging', value: duration(s.imagingSeconds), note: `${s.targets} ${s.targets === 1 ? 'target' : 'targets'}, ${s.projects} ${s.projects === 1 ? 'project' : 'projects'}` },
    { label: 'Subs planned', value: String(s.subs), note: 'merged per target and filter' },
    { label: 'Waits', value: String(s.waits), note: s.waits ? duration(s.waitSeconds) + ' with nothing up' : 'something is up all night' },
  ]
})

const W = 1200
const LABEL_W = 150
const range = computed(() => nightRange(preview.value))
const sc = computed(() => (range.value ? scale(range.value[0], range.value[1], LABEL_W, W) : null))
const bands = computed(() => (sc.value ? twilightBands(preview.value?.twilight, sc.value) : []))
const ticks = computed(() => (sc.value ? hourTicks(sc.value.t0, sc.value.t1).map((t) => ({ x: sc.value!.x(t), label: hm(t) })) : []))
const nowX = computed(() => {
  const s = sc.value
  if (!s || night.value !== 'tonight' || shell.now < s.t0 || shell.now > s.t1) return null
  return s.x(shell.now)
})

const MOON_Y = 170
const moonY = (alt: number) => MOON_Y - Math.max(-44, Math.min(44, alt)) * 0.5
const moonPaths = computed(() => {
  const s = sc.value
  const m = moonNight.value
  if (!s || !m) return { below: '', above: [] as string[], marks: [] as { x: number; label: string; anchor: string }[] }
  const pts = m.samples.map((p) => ({ t: ms(p.t), alt: p.alt })).filter((p) => p.t >= s.t0 && p.t <= s.t1)
  const below = pts.map((p, i) => `${i ? 'L' : 'M'}${s.x(p.t).toFixed(1)} ${moonY(Math.min(0, p.alt)).toFixed(1)}`).join(' ')
  const above: string[] = []
  let seg: string[] = []
  for (const p of pts) {
    if (p.alt >= 0) seg.push(`${seg.length ? 'L' : 'M'}${s.x(p.t).toFixed(1)} ${moonY(p.alt).toFixed(1)}`)
    else if (seg.length) {
      above.push(seg.join(' '))
      seg = []
    }
  }
  if (seg.length) above.push(seg.join(' '))
  const marks: { x: number; label: string; anchor: string }[] = []
  const add = (v: string, word: string) => {
    const t = ms(v)
    if (t < s.t0 || t > s.t1) return
    const x = s.x(t)
    marks.push({ x, label: `${word} ${hm(t)}`, anchor: x > W - 120 ? 'end' : 'start' })
  }
  m.rises.forEach((v) => add(v, 'rises'))
  m.sets.forEach((v) => add(v, 'sets'))
  return { below, above, marks }
})
const moonCaption = computed(() => {
  const r = range.value
  return r ? moonLine(moonNight.value, r[0], r[1]) : ''
})
const moonPhase = computed(() => {
  const r = range.value
  return moonPhaseText(moonNight.value, !!r && noMoonAvoidance(moonNight.value, r[0], r[1]))
})
const phaseText = computed(() => {
  const bar = Math.max(2, (W - LABEL_W) * (moonNight.value?.illumination ?? 0))
  return bar > (W - LABEL_W) * 0.6 ? { x: LABEL_W + 8, fill: 'var(--background)' } : { x: LABEL_W + bar + 8, fill: 'var(--muted-foreground)' }
})
const moonAllDown = computed(() => /below the horizon all night/.test(moonCaption.value))
const svgH = computed(() => (moonNight.value ? 270 : 150))
const bandH = computed(() => svgH.value - 36)

const PALETTE = ['var(--c1)', 'var(--c4)', 'var(--c2)', 'var(--c3)']

function colourFor(r: Run, order: Map<number, number>): string {
  const key = r.project_id ?? r.target_id ?? 0
  if (!order.has(key)) order.set(key, order.size)
  return PALETTE[order.get(key)! % PALETTE.length]
}

interface Bar {
  x: number
  w: number
  colour: string
  wait: boolean
  name: string
  sub: string
  title: string
}

function bars(p: Preview | null, order: Map<number, number>): Bar[] {
  const s = sc.value
  if (!s || !p) return []
  return runs(p.blocks).map((r) => {
    const x = s.x(r.start)
    const w = Math.max(1, s.x(r.end) - x)
    const total = r.pick?.total
    const score = total !== null && total !== undefined ? total.toFixed(2) : ''
    const project = r.project_name && r.project_name !== r.target_name ? r.project_name : ''
    return {
      x,
      w,
      colour: r.wait ? 'var(--muted-foreground)' : colourFor(r, order),
      wait: r.wait,
      name: r.wait ? 'Wait' : (r.target_name ?? ''),
      sub: r.wait ? '' : [project, score].filter(Boolean).join(' · '),
      title: r.wait ? `Wait ${hm(r.start)}–${hm(r.end)}` : `${r.target_name} ${hm(r.start)}–${hm(r.end)}${r.reason ? ' · ' + r.reason : ''}`,
    }
  })
}

const colourOrder = computed(() => new Map<number, number>())
const targetBars = computed(() => bars(preview.value, colourOrder.value))

const filterBars = computed(() => {
  const s = sc.value
  if (!s) return []
  return (preview.value?.blocks ?? [])
    .filter((b) => !b.wait)
    .map((b) => {
      const x = s.x(ms(b.start))
      return { x, w: Math.max(0.5, s.x(ms(b.end)) - x), fill: filterColor(b.filter), title: `${b.filter ?? ''} · ${b.count ?? 0} × ${b.exposure_seconds ?? 0} s` }
    })
    .filter((b) => !isNaN(b.x) && !isNaN(b.w))
})

const legend = [
  { label: 'Luminance', fill: 'var(--lum)' },
  { label: 'Red', fill: 'var(--red)' },
  { label: 'Green', fill: 'var(--green)' },
  { label: 'Blue', fill: 'var(--blue)' },
  { label: 'H-a', fill: 'var(--ha)' },
  { label: 'O-III', fill: 'var(--oiii)' },
  { label: 'S-II', fill: 'var(--sii)' },
]

const timelineLabel = computed(() => {
  const r = runs(preview.value?.blocks).filter((x) => !x.wait)
  const moon = moonCaption.value ? ' ' + moonCaption.value : ''
  if (!r.length) return 'Tonight has nothing planned.' + moon
  return "Tonight's plan: " + r.map((x) => `${x.target_name} from ${hm(x.start)} to ${hm(x.end)}`).join(', ') + '.' + moon
})

const picks = computed(() => pickTable(preview.value?.blocks))
const fmtScore = (v: number | undefined) => (v === undefined ? '—' : v.toFixed(2))
const pct = (w: number) => Math.round(w * 100)

const switchWeight = computed(() => picks.value.columns.find((c) => /switch/i.test(c.rule))?.weight)
const priorityWeight = computed(() => picks.value.columns.find((c) => /priority/i.test(c.rule))?.weight)

const leftOut = computed(() =>
  (preview.value?.left_out ?? []).map((l) => ({
    name: [l.project_name, l.target_name].filter((x, i, a) => x && a.indexOf(x) === i).join(' · ') || 'Unnamed',
    why: l.reason ?? '',
  })),
)

const fields: OverrideField[] = ['priority', 'state', 'minimumtime']
const wiProject = ref<number | null>(null)
const wiField = ref<OverrideField>('priority')
const wiValue = ref<number | null>(null)
const changes = ref<WhatIfChange[]>([])
const whatIf = ref<Preview | null>(null)
const whatIfError = ref('')
const whatIfBusy = ref(false)
const applying = ref(false)

const projectOpts = computed(() => [...projects.value].sort((a, b) => (a.state === 1 ? 0 : 1) - (b.state === 1 ? 0 : 1) || a.name.localeCompare(b.name)))
const wiProj = computed(() => projects.value.find((p) => p.id === wiProject.value) ?? null)
const currentValue = computed(() => (wiProj.value ? fieldValue(wiProj.value, wiField.value) : null))
const choices = computed(() => (currentValue.value === null ? [] : valueChoices(wiField.value, currentValue.value)))

watch(
  projectOpts,
  (opts) => {
    if (wiProject.value === null && opts.length) wiProject.value = opts[0].id
  },
  { immediate: true },
)

watch(
  choices,
  (c) => {
    if (wiValue.value === null || !c.includes(wiValue.value)) wiValue.value = c.length ? c[0] : null
  },
  { immediate: true },
)

const chips = computed(() =>
  changes.value.map((c) => {
    const p = projects.value.find((x) => x.id === c.projectId)
    const from = p ? valueLabel(c.field, fieldValue(p, c.field)) : '?'
    return { key: `${c.projectId}:${c.field}`, label: `${p?.name ?? 'Project ' + c.projectId} · ${FIELD_LABELS[c.field].toLowerCase()} ${from} → ${valueLabel(c.field, c.value)}` }
  }),
)

async function runWhatIf() {
  if (!changes.value.length) {
    whatIf.value = null
    whatIfError.value = ''
    return
  }
  whatIfBusy.value = true
  try {
    whatIf.value = await postPreview(night.value, overrides(changes.value))
    whatIfError.value = ''
  } catch (e) {
    whatIf.value = null
    whatIfError.value = e instanceof Error ? e.message : String(e)
  } finally {
    whatIfBusy.value = false
  }
}

function add() {
  if (wiProject.value === null || wiValue.value === null) return
  changes.value = addChange(changes.value, { projectId: wiProject.value, field: wiField.value, value: wiValue.value })
  runWhatIf()
}

function remove(key: string) {
  changes.value = changes.value.filter((c) => `${c.projectId}:${c.field}` !== key)
  runWhatIf()
}

function discard() {
  changes.value = []
  whatIf.value = null
  whatIfError.value = ''
}

async function apply() {
  const edits = editsFor(changes.value, projects.value)
  if (!edits.length) return
  applying.value = true
  try {
    for (const e of edits) {
      const r = await editEntity('project.edit', e)
      notifyCommand(r)
      const p = projects.value.find((x) => x.id === e.id)
      if (p) {
        for (const c of e.changes) {
          if (c.field === 'priority') p.priority = c.after as number
          if (c.field === 'state') p.state = c.after as number
          if (c.field === 'minimumtime') p.minimumtime = c.after as number
        }
      }
    }
    discard()
  } catch (e) {
    errorToast(e)
  } finally {
    applying.value = false
  }
}

const diff = computed(() => (whatIf.value ? planDiff(preview.value, whatIf.value) : []))
const compareOrder = computed(() => new Map<number, number>())
const compareCurrent = computed(() => bars(preview.value, compareOrder.value))
const compareWhatIf = computed(() => bars(whatIf.value, compareOrder.value))
const compareTicks = computed(() => ticks.value.filter((_, i) => i % 4 === 1))
const unreachable = computed(() => !!error.value)
const balance = computed(() => mosaicBalance(preview.value, whatIf.value, projects.value))
</script>

<template>
  <main class="page wide">
    <div class="page-head">
      <div>
        <p class="eyebrow-line">{{ nightLabel }}</p>
        <h1>Tonight</h1>
        <p class="lede" style="max-width: 72ch">
          Which target and filter runs when, from dusk to dawn, and why the planner picked it. Try a change in What if before you make it for real.
        </p>
      </div>
      <div class="row">
        <label for="night" class="small muted">Night</label>
        <select id="night" v-model="night" class="input">
          <option value="tonight">Tonight, {{ nightSpan(nightOf(shell.now)) }}</option>
          <option value="tomorrow">Tomorrow, {{ nightSpan(new Date(nightOf(shell.now).getTime() + 24 * 3600 * 1000)) }}</option>
        </select>
        <button type="button" class="btn" :disabled="loading" @click="load(true)">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 12a9 9 0 1 1-3-6.7L21 8M21 3v5h-5" /></svg>
          Re-run simulation
        </button>
      </div>
    </div>

    <section v-if="unreachable" class="card" role="status">
      <h2>The plan is not available</h2>
      <p class="empty" style="padding: 0">
        {{ shell.scheduler.reachable === 'unconfigured' ? 'No scheduler API is configured, so the night cannot be simulated.' : error }}
        The planner runs on the observatory PC; this page fills in when it answers.
      </p>
    </section>

    <template v-else>
      <div class="tiles">
        <div v-for="t in tiles" :key="t.label" class="tile">
          <div class="xsmall muted">{{ t.label }}</div>
          <div class="num tile-value">{{ preview ? t.value : '…' }}</div>
          <div class="xsmall muted">{{ preview ? t.note : '' }}</div>
        </div>
      </div>

      <section aria-labelledby="tl-h" class="card">
        <div class="spread">
          <h2 id="tl-h">Timeline</h2>
          <div class="row xsmall muted" style="gap: 0.875rem">
            <span><span class="key" style="background: var(--day); opacity: 0.45"></span>Day and civil twilight</span>
            <span><span class="key" style="background: var(--day); opacity: 0.22"></span>Nautical</span>
            <span><span class="key" style="background: var(--day); opacity: 0.1"></span>Astronomical</span>
            <span v-if="nowX !== null"><span class="key line"></span>Now {{ hm(shell.now) }}</span>
          </div>
        </div>
        <p v-if="!preview" class="empty">{{ loading ? 'Simulating.' : 'No plan yet.' }}</p>
        <div v-else-if="sc" class="scroll-x">
          <svg :viewBox="`0 0 ${W} ${svgH}`" style="width: 100%; min-width: 56rem; display: block" role="img" :aria-label="timelineLabel">
            <rect v-for="(b, i) in bands" :key="'b' + i" :x="b.x" y="22" :width="b.w" :height="bandH" fill="var(--day)" :opacity="b.opacity" />
            <g font-size="11" fill="var(--muted-foreground)" text-anchor="middle">
              <text v-for="t in ticks" :key="'t' + t.x" :x="t.x" y="14">{{ t.label }}</text>
            </g>
            <g stroke="var(--border)" stroke-width="1">
              <line v-for="t in ticks" :key="'l' + t.x" :x1="t.x" y1="22" :x2="t.x" :y2="22 + bandH" />
            </g>
            <g font-size="12" fill="var(--muted-foreground)">
              <text x="0" y="72">Target</text>
              <text x="0" y="116">Filter</text>
              <template v-if="moonNight">
                <text x="0" y="170">Moon altitude</text>
                <text x="0" y="238">Moon phase</text>
              </template>
            </g>
            <g v-if="moonNight && sc">
              <line :x1="LABEL_W" :y1="MOON_Y" :x2="W" :y2="MOON_Y" stroke="var(--muted-foreground)" stroke-width="1" />
              <text :x="LABEL_W + 6" :y="MOON_Y - 6" font-size="10" fill="var(--muted-foreground)">horizon</text>
              <path :d="moonPaths.below" fill="none" stroke="var(--muted-foreground)" stroke-width="1.5" stroke-dasharray="4 4" />
              <path v-for="(d, i) in moonPaths.above" :key="'ma' + i" :d="d" fill="none" stroke="var(--foreground)" stroke-width="2" />
              <template v-for="(mk, i) in moonPaths.marks" :key="'mk' + i">
                <circle :cx="mk.x" :cy="MOON_Y" r="3.5" fill="var(--foreground)" />
                <text :x="mk.anchor === 'end' ? mk.x - 6 : mk.x + 6" :y="MOON_Y - 14" font-size="10" :text-anchor="mk.anchor" fill="var(--foreground)">{{ mk.label }}</text>
              </template>
              <text v-if="moonAllDown" :x="(LABEL_W + W) / 2" y="208" font-size="10" text-anchor="middle" fill="var(--muted-foreground)">below horizon all night</text>
              <rect :x="LABEL_W" y="226" :width="W - LABEL_W" height="16" rx="3" fill="var(--secondary)" />
              <rect :x="LABEL_W" y="226" :width="Math.max(2, (W - LABEL_W) * moonNight.illumination)" height="16" rx="3" fill="var(--foreground)" opacity="0.85" />
              <text :x="phaseText.x" y="238" font-size="10" :fill="phaseText.fill">{{ moonPhase }}</text>
            </g>
            <g v-for="(b, i) in targetBars" :key="'tb' + i">
              <title>{{ b.title }}</title>
              <rect
                :x="b.x"
                y="40"
                :width="b.w"
                height="56"
                rx="6"
                :fill="b.wait ? 'none' : b.colour"
                :fill-opacity="b.wait ? undefined : 0.3"
                :stroke="b.colour"
                :stroke-dasharray="b.wait ? '3 3' : undefined"
              />
              <template v-if="b.w > 36">
                <text :x="b.x + 8" y="63" :font-size="b.w > 90 ? 13 : 11" font-weight="600" :fill="b.wait ? 'var(--muted-foreground)' : 'var(--foreground)'">{{ b.name }}</text>
                <text v-if="b.sub && b.w > 70" :x="b.x + 8" y="82" font-size="11" fill="var(--muted-foreground)">{{ b.sub }}</text>
              </template>
            </g>
            <g>
              <rect v-for="(f, i) in filterBars" :key="'f' + i" :x="f.x" y="104" :width="f.w" height="16" :fill="f.fill"><title>{{ f.title }}</title></rect>
            </g>
            <template v-if="nowX !== null">
              <line :x1="nowX" y1="22" :x2="nowX" :y2="22 + bandH" stroke="var(--foreground)" stroke-width="2" />
              <text :x="nowX + 4" y="34" font-size="11" font-weight="600" fill="var(--foreground)">now</text>
            </template>
          </svg>
        </div>
        <p v-else class="empty">The plan has no times to draw.</p>
        <div class="row xsmall muted" style="gap: 0.875rem">
          <span v-for="l in legend" :key="l.label"><span class="key" :style="{ background: l.fill, borderRadius: 0 }"></span>{{ l.label }}</span>
          <span>Twilight times are for the observatory site, rounded to the minute.</span>
          <span v-if="moonError">The Moon is not drawn: {{ moonError }}</span>
        </div>
      </section>
    </template>

    <div class="split">
      <section v-if="!unreachable" aria-labelledby="why-h" class="card why">
        <div>
          <h2 id="why-h">Why each target was picked</h2>
          <p class="small muted" style="margin: 0.25rem 0 0; max-width: 75ch">
            Score = Σ rule weight × rule score, at the moment of each pick. A picked target then earns
            {{ switchWeight !== undefined ? '+' + switchWeight.toFixed(2) : 'a bonus' }} from Target switch penalty at every re-plan, so it only gives way when it
            sets or its plans complete. Normal priority adds {{ priorityWeight !== undefined ? (priorityWeight * 0.5).toFixed(2) : 'half the priority weight' }}, which can
            never beat that.
          </p>
        </div>
        <div v-if="picks.rows.length" class="scroll-x framed">
          <table class="grid num">
            <thead>
              <tr>
                <th scope="col">Picked</th>
                <th scope="col">Target</th>
                <th v-for="c in picks.columns" :key="c.rule" scope="col" style="text-align: right">{{ c.rule }} ×{{ pct(c.weight) }}</th>
                <th scope="col" style="text-align: right">Total</th>
                <th scope="col">Runner-up</th>
                <th scope="col">Gives way because</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(p, i) in picks.rows" :key="i">
                <td style="white-space: nowrap">{{ p.time }}</td>
                <td style="white-space: nowrap; font-weight: 600">{{ p.target }}</td>
                <td v-for="c in picks.columns" :key="c.rule" style="text-align: right" :class="{ muted: !c.weight }">{{ fmtScore(p.scores[c.rule]) }}</td>
                <td style="text-align: right; font-weight: 600">{{ p.total === null ? '—' : p.total.toFixed(2) }}</td>
                <td class="muted" style="white-space: nowrap">{{ p.runner }}</td>
                <td class="muted">{{ p.ends }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="empty">{{ preview ? 'No picks in this plan.' : 'Loading.' }}</p>
        <div>
          <h3 class="h3">Left out tonight</h3>
          <ul v-if="leftOut.length" class="left-out">
            <li v-for="(l, i) in leftOut" :key="i">
              <div style="font-weight: 600">{{ l.name }}</div>
              <div class="muted">{{ l.why }}</div>
            </li>
          </ul>
          <p v-else class="empty" style="padding: 0">{{ preview ? 'Every active project gets time.' : '' }}</p>
        </div>
      </section>

      <section aria-labelledby="whatif-h" class="card whatif">
        <div>
          <h2 id="whatif-h">What if</h2>
          <p class="small muted" style="margin: 0.25rem 0 0">Changes here run on a copy of tonight. Nothing reaches the observatory until you apply them.</p>
        </div>
        <p v-if="projectsError" class="empty" style="padding: 0">The project list is not available: {{ projectsError }}</p>
        <p v-else-if="!projects.length" class="empty" style="padding: 0">The scheduler database has no projects.</p>
        <template v-else>
          <div class="wi-grid">
            <label for="wi-project" class="field">
              <span>Project</span>
              <select id="wi-project" v-model="wiProject" class="input">
                <option v-for="p in projectOpts" :key="p.id" :value="p.id">{{ p.name }}</option>
              </select>
            </label>
            <label for="wi-field" class="field">
              <span>Change</span>
              <select id="wi-field" v-model="wiField" class="input">
                <option v-for="f in fields" :key="f" :value="f">{{ FIELD_LABELS[f] }}</option>
              </select>
            </label>
            <label for="wi-value" class="field">
              <span>From {{ currentValue === null ? '?' : valueLabel(wiField, currentValue) }} to</span>
              <select id="wi-value" v-model="wiValue" class="input">
                <option v-for="v in choices" :key="v" :value="v">{{ valueLabel(wiField, v) }}</option>
              </select>
            </label>
          </div>
          <div class="row">
            <span v-for="c in chips" :key="c.key" class="badge">
              {{ c.label }}
              <button type="button" class="chip-x" aria-label="Remove this change" @click="remove(c.key)">
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" aria-hidden="true"><path d="M18 6 6 18M6 6l12 12" /></svg>
              </button>
            </span>
            <span v-if="!chips.length" class="xsmall muted">No changes yet. Pick one above and add it.</span>
            <button type="button" class="add-chip" :disabled="wiValue === null || unreachable" @click="add">Add this change</button>
          </div>
          <p v-if="unreachable" class="xsmall muted" style="margin: 0">What if needs the planner on the observatory PC, which is not answering.</p>
          <p v-if="whatIfError" class="empty" style="padding: 0">The what-if plan failed: {{ whatIfError }}</p>
          <p v-else-if="whatIfBusy" class="xsmall muted" style="margin: 0">Simulating the change.</p>
          <div v-if="whatIf && sc" class="scroll-x">
            <svg :viewBox="`0 0 ${W} 150`" style="width: 100%; min-width: 34rem; display: block" role="img" aria-label="Current plan compared with the what-if plan">
              <g font-size="22" fill="var(--muted-foreground)" text-anchor="middle">
                <text v-for="t in compareTicks" :key="'ct' + t.x" :x="t.x" y="20">{{ t.label }}</text>
              </g>
              <text x="0" y="62" font-size="22" fill="var(--muted-foreground)">Current</text>
              <text x="0" y="122" font-size="22" fill="var(--foreground)" font-weight="600">What if</text>
              <g v-for="(b, i) in compareCurrent" :key="'cc' + i">
                <rect :x="b.x" y="34" :width="b.w" height="44" rx="6" :fill="b.wait ? 'none' : b.colour" :fill-opacity="b.wait ? undefined : 0.3" :stroke="b.colour" :stroke-dasharray="b.wait ? '3 3' : undefined" />
                <text v-if="b.w > 110 && !b.wait" :x="b.x + 8" y="63" font-size="20" fill="var(--foreground)">{{ b.name }}</text>
              </g>
              <g v-for="(b, i) in compareWhatIf" :key="'cw' + i">
                <rect :x="b.x" y="94" :width="b.w" height="44" rx="6" :fill="b.wait ? 'none' : b.colour" :fill-opacity="b.wait ? undefined : 0.3" :stroke="b.colour" stroke-width="2" :stroke-dasharray="b.wait ? '3 3' : undefined" />
                <text v-if="b.w > 110 && !b.wait" :x="b.x + 8" y="123" font-size="20" fill="var(--foreground)">{{ b.name }}</text>
              </g>
            </svg>
          </div>
          <ul v-if="diff.length" class="diff num">
            <li v-for="(d, i) in diff" :key="i">
              <span>{{ d.name }}</span><span style="font-weight: 600">{{ d.change }}</span>
            </li>
          </ul>
          <div v-for="b in balance" :key="b.projectGuid" role="status" class="balance">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="var(--warn)" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" style="flex: none; margin-top: 2px"><path d="M12 9v4M12 17h.01M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z" /></svg>
            <span>{{ b.text }} <RouterLink :to="{ name: 'mosaic', params: { projectId: b.projectGuid }, query: { tab: 'panels' } }" class="underline">Review balancing</RouterLink></span>
          </div>
          <div class="actions">
            <button type="button" class="btn" :disabled="!changes.length" @click="discard">Discard</button>
            <button type="button" class="btn primary" :disabled="!changes.length || applying" @click="apply">Apply after this exposure</button>
          </div>
        </template>
      </section>
    </div>
  </main>
</template>

<style scoped>
.eyebrow-line {
  margin: 0;
  font-size: 0.875rem;
  color: var(--muted-foreground);
}
.tiles {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 11rem), 1fr));
  gap: 0.75rem;
}
.tile {
  border: 1px solid var(--border);
  border-radius: 0.625rem;
  padding: 0.75rem 1rem;
}
.tile-value {
  font-size: 1.125rem;
  font-weight: 600;
}
.key {
  display: inline-block;
  width: 0.75rem;
  height: 0.75rem;
  border-radius: 2px;
  vertical-align: -2px;
  margin-right: 0.25rem;
}
.key.line {
  height: 2px;
  background: var(--foreground);
  vertical-align: 3px;
}
.split {
  display: flex;
  flex-wrap: wrap;
  gap: 1.5rem;
  align-items: flex-start;
}
.why {
  flex: 3 1 40rem;
}
.whatif {
  flex: 2 1 26rem;
}
.framed {
  border: 1px solid var(--border);
  border-radius: 0.5rem;
}
.framed table.grid tr:last-child td {
  border-bottom: 0;
}
.h3 {
  margin: 0 0 0.5rem;
  font-size: 0.875rem;
  font-weight: 600;
}
.left-out {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 17rem), 1fr));
  gap: 0.5rem;
}
.left-out li {
  background: var(--secondary);
  border-radius: 0.5rem;
  padding: 0.5rem 0.75rem;
  font-size: 0.8125rem;
}
.wi-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 10rem), 1fr));
  gap: 0.75rem;
}
.wi-grid .field {
  font-size: 0.8125rem;
}
.chip-x {
  border: 0;
  background: transparent;
  padding: 0;
  display: inline-flex;
}
.add-chip {
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
  border: 1px dashed var(--border);
  border-radius: 0.375rem;
  padding: 0.125rem 0.5rem;
  font-size: 0.75rem;
  background: transparent;
}
.diff {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 0.375rem;
  font-size: 0.8125rem;
}
.diff li {
  display: flex;
  justify-content: space-between;
  gap: 0.75rem;
  border-bottom: 1px solid var(--border);
  padding-bottom: 0.375rem;
}
.balance {
  display: flex;
  gap: 0.5rem;
  align-items: flex-start;
  background: var(--warn-bg);
  border-radius: 0.5rem;
  padding: 0.625rem 0.75rem;
  font-size: 0.8125rem;
}
.underline {
  text-decoration: underline;
  text-underline-offset: 2px;
}
.actions {
  display: flex;
  gap: 0.5rem;
  justify-content: flex-end;
  flex-wrap: wrap;
}
</style>
