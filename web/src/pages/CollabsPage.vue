<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { MONTHS, decText, filterColour, getCollabs, hours, raText, type Collab, type CollabsView, type Criterion } from '../api/discover'
import { ago, hm, shortDate } from '../format'
import { errorToast, shell } from '../shell'

const view = ref<CollabsView | null>(null)
const loading = ref(true)
const f = reactive({ fits: false, mosaic: false, closed: true })
const expanded = reactive<Record<string, boolean>>({})

onMounted(async () => {
  try {
    view.value = await getCollabs()
  } catch (e) {
    errorToast(e, 'Could not load the collaborations')
  } finally {
    loading.value = false
  }
})

const keep = (c: Collab) => (!f.fits || c.fits) && (!f.mosaic || c.kind === 'mosaic')
const open = computed(() => (view.value?.open ?? []).filter(keep))
const closed = computed(() => (view.value?.closed ?? []).filter(keep))
const hiddenOpen = computed(() => (view.value?.open.length ?? 0) - open.value.length)

const rigLine = computed(() => {
  const r = view.value?.rig
  if (!r) return ''
  return `${r.focalLength} mm · ${r.scale.toFixed(3)}″/px · ${r.widthDeg.toFixed(2)}° × ${r.heightDeg.toFixed(2)}° · ${r.colour ? 'colour' : 'mono'} · ${(r.filters ?? []).join(' ')}`
})

function icon(c: Criterion) {
  return c.result === 'pass' ? 'M5 12l5 5L20 7' : c.result === 'fail' ? 'M18 6 6 18M6 6l12 12' : 'M5 12h14'
}
function tone(c: Criterion) {
  return c.result === 'pass' ? 'var(--ok)' : c.result === 'fail' ? 'var(--bad)' : 'var(--muted-foreground)'
}

function goals(c: Collab) {
  const order = ['H', 'O', 'S', 'R', 'G', 'B', 'L']
  return Object.keys(c.goals)
    .sort((a, b) => order.indexOf(a) - order.indexOf(b))
    .map((k) => {
      const goal = c.goals[k]!
      const done = c.progress[k] ?? 0
      return { name: k, goal, done, pct: Math.min(100, (done / goal) * 100), colour: filterColour(k) }
    })
}

function totals(c: Collab) {
  const g = Object.values(c.goals).reduce((a, b) => a + b, 0)
  const d = Object.values(c.progress).reduce((a, b) => a + b, 0)
  return `${d.toFixed(1)} of ${g.toLocaleString()} h`
}

function have(c: Collab) {
  const sum: Record<string, number> = {}
  for (const h of c.have) for (const [k, v] of Object.entries(h.hours)) sum[k] = (sum[k] ?? 0) + v
  const max = Math.max(1, ...Object.values(sum))
  const order = ['H', 'O', 'S', 'R', 'G', 'B', 'L']
  return order
    .filter((k) => k in c.goals || k in sum)
    .map((k) => ({ name: k, hours: sum[k] ?? 0, pct: ((sum[k] ?? 0) / max) * 100, colour: filterColour(k) }))
}

function region(c: Collab) {
  const r = c.region
  const rig = view.value?.rig
  const fw = rig?.widthDeg ?? 3.32
  const fh = rig?.heightDeg ?? 2.22
  const diag = Math.hypot(fw, fh)
  const s = Math.min(360 / Math.max(r.width, diag), 240 / Math.max(r.height, diag))
  return { rw: r.width * s, rh: r.height * s, fw: fw * s, fh: fh * s, rot: r.rotation || 0 }
}

function crit(c: Collab) {
  return c.criteria.filter((x) => x.result !== 'open')
}

function why(c: Collab) {
  const bad = c.criteria.filter((x) => x.result === 'fail')
  const list = bad.length ? bad : c.criteria.filter((x) => x.result === 'pass')
  return list.map((x) => `${x.name} ${x.rule}`).join(' · ') || 'No limits set'
}

function kindText(c: Collab) {
  if (c.kind !== 'mosaic') return 'Single field'
  return `Mosaic · ≈ ${c.panels} of your frames`
}

function chart(c: Collab) {
  const pts = c.curve ?? []
  if (pts.length < 2) return null
  const t0 = new Date(pts[0]!.at).getTime()
  const t1 = new Date(pts[pts.length - 1]!.at).getTime()
  const x = (t: number) => 40 + ((t - t0) / (t1 - t0)) * 540
  const y = (a: number) => 150 - (Math.max(0, a) / 90) * 140
  const d = pts.map((p, i) => `${i ? 'L' : 'M'}${x(new Date(p.at).getTime()).toFixed(1)} ${y(p.alt).toFixed(1)}`).join(' ')
  const peak = pts.reduce((a, b) => (b.alt > a.alt ? b : a))
  const n = view.value?.night
  const dusk = n?.dusk ? x(new Date(n.dusk).getTime()) : 40
  const dawn = n?.dawn ? x(new Date(n.dawn).getTime()) : 580
  const ticks: { x: number; label: string }[] = []
  const first = Math.ceil(t0 / 7200000) * 7200000
  for (let t = first; t <= t1; t += 7200000) ticks.push({ x: x(t), label: hm(t) })
  return { d, peakX: x(new Date(peak.at).getTime()), peakY: y(peak.alt), peak, dusk, dawn, ticks, minY: y(n?.minAltitude ?? 30) }
}

function monthBars(c: Collab) {
  return c.months.map((v) => ({ h: Math.max(2, Math.round((Math.min(v, 9) / 9) * 36)) + 'px', c: v >= 4 ? 'var(--bar)' : 'var(--bar-off)' }))
}

function bestMonths(c: Collab) {
  const best = c.months.map((v, i) => (v >= 4 ? MONTHS[i] : '')).filter(Boolean)
  return best.length ? 'Best months ' + best.join(', ') : 'No month with four dark hours'
}
</script>

<template>
  <main class="page wide">
    <div class="page-head">
      <div>
        <p class="eyebrow">Discover</p>
        <h1>Collabs</h1>
        <p class="lede">
          Starfront collaborations, read from collab.starfront.space by the server. Read only: nothing here talks to
          Starfront on your behalf.
        </p>
      </div>
      <span v-if="view" class="xsmall muted num">
        <template v-if="view.fetchedAt">Last fetched {{ ago(view.fetchedAt, shell.now) }}</template>
        <template v-else-if="view.enabled">Not fetched yet</template>
        <template v-else>Starfront reading is off</template>
        · {{ view.open.length }} open · {{ view.closedTotal }} closed
        <template v-if="view.sky"> · {{ view.sky.online }} of {{ view.sky.telescopes }} Starfront telescopes online</template>
      </span>
    </div>

    <p v-if="loading" class="empty">Loading collaborations…</p>
    <p v-if="view?.error" class="badge warn" style="align-self: flex-start">Last fetch failed: {{ view.error }}</p>
    <p v-if="view && !view.enabled" class="empty">Set discover.starfront to true to read Starfront's public collaboration list.</p>

    <div v-if="view" role="group" aria-label="Filters" class="row">
      <button type="button" class="chip" :class="{ on: f.fits }" :aria-pressed="f.fits" @click="f.fits = !f.fits">Fits my rig</button>
      <button type="button" class="chip" :class="{ on: f.mosaic }" :aria-pressed="f.mosaic" @click="f.mosaic = !f.mosaic">Mosaic only</button>
      <label for="c-closed" class="row small" style="gap: 0.375rem; margin-left: 0.25rem"><input id="c-closed" v-model="f.closed" type="checkbox" /> Show closed in the last 30 days</label>
    </div>

    <article v-for="c in open" :key="c.id" :aria-labelledby="'c-' + c.id" class="card" style="gap: 1.25rem">
      <div class="spread" style="align-items: flex-start; gap: 1rem">
        <div>
          <div class="row">
            <span class="badge ok"><svg width="7" height="7" viewBox="0 0 8 8" aria-hidden="true"><circle cx="4" cy="4" r="4" fill="currentColor" /></svg>Open</span>
            <span class="badge">{{ kindText(c) }}</span>
          </div>
          <h2 :id="'c-' + c.id" style="margin: 0.5rem 0 0; font-size: 1.375rem; line-height: 1.3">{{ c.name }}</h2>
          <p class="small muted" style="margin: 0.125rem 0 0">
            Coordinator {{ c.coordinator }} · opened {{ shortDate(c.created) }}<template v-if="c.notes"> · “{{ c.notes }}”</template>
            <template v-if="c.summary"> · {{ c.summary.joined }} telescopes joined, {{ c.summary.declined }} declined · {{ c.summary.reporters }} have reported</template>
          </p>
        </div>
        <div style="display: flex; flex-direction: column; align-items: flex-end; gap: 0.375rem">
          <span :class="['badge', c.fits ? 'ok' : 'bad']" style="font-size: 0.875rem; padding: 0.375rem 0.75rem">
            {{ c.fits ? 'Fits your rig · every criterion passes' : 'Does not fit your rig' }}
          </span>
          <span class="xsmall muted">Joining needs a Starfront sign-in — not set up</span>
        </div>
      </div>

      <div class="row" style="gap: 1.5rem; align-items: flex-start">
        <div style="flex: 1 1 20rem; display: flex; flex-direction: column; gap: 0.625rem; min-width: 0">
          <svg
            viewBox="0 0 400 280"
            role="img"
            :aria-label="`Collaboration region, ${c.region.width.toFixed(2)} by ${c.region.height.toFixed(2)} degrees${c.near ? ' around ' + c.near : ''}, with your frame at the requested camera angle covering about ${Math.round(c.coverage * 100)} percent of it`"
            class="sky"
          >
            <rect x="0" y="0" width="400" height="280" fill="var(--sky)" />
            <rect :x="200 - region(c).rw / 2" :y="140 - region(c).rh / 2" :width="region(c).rw" :height="region(c).rh" fill="none" stroke="var(--warn)" stroke-width="2" stroke-dasharray="6 4" />
            <text :x="200 - region(c).rw / 2 + 6" :y="Math.max(14, 140 - region(c).rh / 2 - 6)" font-size="11" fill="var(--warn)">collab region {{ c.region.width.toFixed(2) }}° × {{ c.region.height.toFixed(2) }}°</text>
            <rect
              :x="200 - region(c).fw / 2"
              :y="140 - region(c).fh / 2"
              :width="region(c).fw"
              :height="region(c).fh"
              fill="#f4f4f8"
              fill-opacity="0.06"
              stroke="#f4f4f8"
              stroke-width="1.5"
              :transform="`rotate(${region(c).rot} 200 140)`"
            />
            <text x="12" y="268" font-size="11" fill="#f4f4f8">your frame at {{ c.region.rotation.toFixed(1) }}°</text>
            <text v-if="c.near" x="206" y="136" font-size="11" fill="#f4f4f8">{{ c.near }}</text>
          </svg>
          <dl class="facts">
            <div><dt>Centre</dt><dd class="num">{{ raText(c.region.ra) }} · {{ decText(c.region.dec) }}</dd></div>
            <div><dt>Camera angle</dt><dd class="num">{{ c.region.rotation.toFixed(1) }}°</dd></div>
            <div><dt>Your coverage</dt><dd>{{ c.kind === 'mosaic' ? `${c.panels} frames (${c.columns} × ${c.rows})` : `≈ ${Math.round(c.coverage * 100)}% in one frame` }}</dd></div>
            <div><dt>Deal for you</dt><dd>{{ c.kind === 'mosaic' ? 'cells of your own frame size' : '1 cell, centred' }}</dd></div>
          </dl>
        </div>

        <div style="flex: 2 1 30rem; display: flex; flex-direction: column; gap: 0.75rem; min-width: 0">
          <div class="spread">
            <h3 class="h3">Goals and progress</h3>
            <span class="xsmall muted">{{ totals(c) }} · accepted contributions<template v-if="c.summary?.firstNight">, {{ c.summary.firstNight }} – {{ c.summary.lastNight }}</template></span>
          </div>
          <div v-for="g in goals(c)" :key="g.name" class="goal small">
            <span class="row" style="gap: 0.375rem; font-weight: 600"><span class="key" :style="{ background: g.colour }" />{{ g.name }}</span>
            <div class="track"><div :style="{ width: g.pct + '%', minWidth: g.done > 0 ? '3px' : '0', background: g.colour }" /></div>
            <span class="num" style="text-align: right"><strong>{{ g.done.toFixed(1) }}</strong> / {{ g.goal }} h</span>
          </div>
          <p class="xsmall muted" style="margin: 0">
            Starfront counts open-shutter hours from each telescope's nightly report. Your own figures elsewhere are effective
            hours, so they are not directly comparable.
          </p>
        </div>
      </div>

      <div class="panels">
        <section class="sub">
          <h3 class="h3">Criteria against your rig</h3>
          <p class="xsmall muted" style="margin: 0">{{ rigLine }}</p>
          <ul class="crit">
            <li v-for="x in c.criteria" :key="x.name">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" :stroke="tone(x)" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path :d="icon(x)" /></svg>
              <span><span style="font-weight: 500">{{ x.name }}</span> <span class="muted">{{ x.rule }}</span></span>
              <span class="num" :style="{ color: tone(x), textAlign: 'right' }">{{ x.you }}</span>
            </li>
          </ul>
        </section>

        <section class="sub">
          <div class="spread">
            <h3 class="h3">Tonight from your site</h3>
            <span v-if="c.tonight" class="xsmall muted">≈ {{ hours(c.tonight.hours) }} above {{ view?.night?.minAltitude ?? 30 }}° in the dark</span>
          </div>
          <p v-if="!chart(c)" class="empty">{{ view?.siteError || 'No altitude curve yet.' }}</p>
          <svg v-else viewBox="0 0 600 175" role="img" :aria-label="`Altitude of the collaboration centre tonight, highest at ${Math.round(chart(c)!.peak.alt)} degrees around ${hm(chart(c)!.peak.at)}`" style="width: 100%; display: block">
            <rect x="40" y="10" :width="Math.max(0, chart(c)!.dusk - 40)" height="140" fill="var(--day)" opacity="0.18" />
            <rect :x="chart(c)!.dawn" y="10" :width="Math.max(0, 580 - chart(c)!.dawn)" height="140" fill="var(--day)" opacity="0.18" />
            <g stroke="var(--border)" stroke-width="1">
              <line x1="40" y1="150" x2="580" y2="150" />
              <line x1="40" :y1="chart(c)!.minY" x2="580" :y2="chart(c)!.minY" stroke-dasharray="3 4" />
              <line x1="40" y1="56.7" x2="580" y2="56.7" stroke-dasharray="3 4" />
              <line x1="40" y1="10" x2="580" y2="10" stroke-dasharray="3 4" />
            </g>
            <g font-size="11" fill="var(--muted-foreground)" text-anchor="end">
              <text x="34" y="154">0°</text><text x="34" :y="chart(c)!.minY + 4">{{ view?.night?.minAltitude ?? 30 }}°</text><text x="34" y="61">60°</text><text x="34" y="14">90°</text>
            </g>
            <g font-size="11" fill="var(--muted-foreground)" text-anchor="middle">
              <text v-for="t in chart(c)!.ticks" :key="t.label" :x="t.x" y="168">{{ t.label }}</text>
            </g>
            <path :d="chart(c)!.d" fill="none" stroke="var(--bar)" stroke-width="2" />
            <circle :cx="chart(c)!.peakX" :cy="chart(c)!.peakY" r="3.5" fill="var(--bar)" />
            <text :x="Math.min(chart(c)!.peakX + 6, 520)" :y="Math.max(chart(c)!.peakY - 3, 22)" font-size="11" fill="var(--foreground)">{{ Math.round(chart(c)!.peak.alt) }}° at {{ hm(chart(c)!.peak.at) }}</text>
          </svg>
          <div>
            <div class="xsmall muted" style="margin-bottom: 0.25rem">{{ bestMonths(c) }}</div>
            <div role="img" :aria-label="bestMonths(c)" class="mgrid" style="align-items: end; height: 2.25rem">
              <span v-for="(b, i) in monthBars(c)" :key="i" :style="{ height: b.h, background: b.c, borderRadius: '2px' }" />
            </div>
            <div class="mgrid xsmall muted" style="text-align: center; margin-top: 0.125rem">
              <span v-for="m in MONTHS" :key="m">{{ m[0] }}</span>
            </div>
          </div>
        </section>

        <section class="sub">
          <h3 class="h3">You already have</h3>
          <p v-if="!c.have.length" class="small muted" style="margin: 0">Nothing of yours overlaps this region yet.</p>
          <template v-else>
            <p class="small" style="margin: 0">
              Your
              <template v-for="(h, i) in c.have" :key="h.subject">
                <RouterLink v-if="h.projectId" :to="{ name: 'target', params: { projectId: String(h.projectId) } }" class="lnk" style="text-decoration: underline">{{ h.name }}</RouterLink><span v-else>{{ h.name }}</span><span v-if="i < c.have.length - 1">, </span>
              </template>
              {{ c.have.length === 1 ? 'masters overlap' : 'masters overlap' }} this region: {{ c.have.reduce((a, h) => a + h.subs, 0) }} subs stacked<template v-if="c.have[0]!.lastNight">, last on {{ shortDate(c.have[0]!.lastNight) }}</template>.
            </p>
            <div v-for="h in have(c)" :key="h.name" class="havebar small num">
              <span class="row" style="gap: 0.375rem; font-weight: 600"><span class="key" :style="{ background: h.colour }" />{{ h.name }}</span>
              <div class="track thin"><div :style="{ width: h.pct + '%', background: h.colour }" /></div>
              <span style="text-align: right">{{ h.hours ? hours(h.hours) : 'none' }}</span>
            </div>
            <p class="xsmall muted" style="margin: 0">Effective hours from the stacker. Starfront has no way to share subs today, so these stay yours.</p>
          </template>
        </section>
      </div>
    </article>
    <p v-if="hiddenOpen > 0" class="small muted" style="margin: 0">{{ hiddenOpen }} open {{ hiddenOpen === 1 ? 'collab is' : 'collabs are' }} hidden by these filters.</p>
    <p v-if="view && view.enabled && !view.open.length && view.fetchedAt" class="empty">No collaboration is open right now.</p>

    <section v-if="view && f.closed" aria-labelledby="closed-h" class="card" style="gap: 0.875rem">
      <div class="spread">
        <h2 id="closed-h">Recently closed</h2>
        <span class="xsmall muted">{{ closed.length }} of {{ view.closed.length }} shown<template v-if="f.fits"> · fits your rig only</template></span>
      </div>
      <p v-if="!closed.length" class="empty">No closed collab matches these filters.</p>
      <div v-else class="scroll-x" style="border: 1px solid var(--border); border-radius: 0.5rem">
        <table class="grid num">
          <thead>
            <tr>
              <th scope="col">Collaboration</th>
              <th scope="col">Kind</th>
              <th scope="col">Region</th>
              <th scope="col">Goals</th>
              <th scope="col">Fit for your rig</th>
              <th scope="col"><span class="sr-only">Reasons</span></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="c in closed" :key="c.id">
              <td style="white-space: nowrap">
                <div style="font-weight: 600">{{ c.name }}</div>
                <div class="xsmall muted">{{ c.coordinator }} · closed · opened {{ shortDate(c.created) }}</div>
              </td>
              <td style="white-space: nowrap">{{ kindText(c) }}</td>
              <td style="white-space: nowrap">{{ c.region.width.toFixed(1) }}° × {{ c.region.height.toFixed(1) }}°<template v-if="c.near"> around {{ c.near }}</template></td>
              <td style="white-space: nowrap">{{ Object.entries(c.goals).map(([k, v]) => `${k} ${v}`).join(' · ') || '—' }} h</td>
              <td>
                <span :class="['badge', c.fits ? 'ok' : 'bad']">{{ c.fits ? 'Fits' : 'Does not fit' }}</span>
                <div class="xsmall muted">{{ why(c) }}</div>
                <ul v-if="expanded[c.id]" class="crit xsmall" style="margin-top: 0.375rem">
                  <li v-for="x in crit(c)" :key="x.name">
                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" :stroke="tone(x)" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path :d="icon(x)" /></svg>
                    <span><span style="font-weight: 500">{{ x.name }}</span> <span class="muted">{{ x.rule }}</span></span>
                    <span :style="{ color: tone(x), whiteSpace: 'nowrap' }">{{ x.you }}</span>
                  </li>
                </ul>
              </td>
              <td style="text-align: right">
                <button type="button" class="btn sm" :aria-expanded="!!expanded[c.id]" @click="expanded[c.id] = !expanded[c.id]">{{ expanded[c.id] ? 'Hide reasons' : 'Why' }}</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <p class="small muted note">
      Joining needs a Starfront sign-in — not set up. Taking part would mean a Discord sign-in to the Starfront guild and an
      enrolled telescope; ask Starfront before any client but theirs reports.
    </p>
  </main>
</template>

<style scoped>
.chip {
  display: inline-flex;
  align-items: center;
  height: 2rem;
  padding: 0 0.75rem;
  border: 1px solid var(--input);
  border-radius: 999px;
  background: transparent;
  font-size: 0.8125rem;
}
.chip.on {
  border-color: var(--foreground);
  background: var(--sel);
  font-weight: 600;
}
.sky {
  width: 100%;
  aspect-ratio: 10 / 7;
  border-radius: 0.5rem;
  background: var(--sky);
  display: block;
}
.facts {
  margin: 0;
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0.25rem 1rem;
  font-size: 0.8125rem;
}
.facts dt {
  font-size: 0.75rem;
  color: var(--muted-foreground);
}
.facts dd {
  margin: 0;
  font-weight: 600;
}
.h3 {
  margin: 0;
  font-size: 0.9375rem;
  font-weight: 600;
}
.goal {
  display: grid;
  grid-template-columns: 4.5rem 1fr 8rem;
  gap: 0.75rem;
  align-items: center;
}
.havebar {
  display: grid;
  grid-template-columns: 4.5rem 1fr 4.5rem;
  gap: 0.625rem;
  align-items: center;
}
.key {
  display: inline-block;
  width: 0.625rem;
  height: 0.625rem;
  border-radius: 2px;
}
.track {
  height: 0.625rem;
  border-radius: 999px;
  background: var(--secondary);
  overflow: hidden;
}
.track.thin {
  height: 0.5rem;
}
.track div {
  height: 100%;
}
.panels {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 22rem), 1fr));
  gap: 1.25rem;
  align-items: start;
}
.sub {
  border: 1px solid var(--border);
  border-radius: 0.625rem;
  padding: 1rem;
  display: flex;
  flex-direction: column;
  gap: 0.625rem;
  min-width: 0;
}
.crit {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
}
.crit li {
  display: grid;
  grid-template-columns: 1.25rem minmax(0, 1fr) auto;
  gap: 0.5rem;
  align-items: baseline;
  padding: 0.3125rem 0;
  border-top: 1px solid var(--border);
  font-size: 0.8125rem;
}
.mgrid {
  display: grid;
  grid-template-columns: repeat(12, minmax(0, 1fr));
  gap: 3px;
}
.note {
  margin: 0;
  border: 1px dashed var(--border);
  border-radius: 0.625rem;
  padding: 0.75rem 1rem;
}
</style>
