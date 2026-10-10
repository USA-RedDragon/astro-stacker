<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { frame, project, type Framing, type FramingOption, type SkyPoint } from '../api/mosaics'

interface PlannerObject {
  id: string
  name: string
  ra: number
  dec: number
  majorArcmin: number
  minorArcmin: number
  pa: number
}

export interface FramePlan {
  rotation: number
  overlap: number
  layoutName: string
  layoutId: string
  cols: number
  rows: number
  panels: { name?: string; raHours: number; dec: number; rotation: number }[]
}

const props = defineProps<{
  object: PlannerObject
  step: 'frame' | 'mosaic'
  rotation?: number
  panels?: number
  cols?: number
  rows?: number
}>()

const emit = defineEmits<{ 'update:plan': [plan: FramePlan] }>()

const W = 640
const H = 400

const rotation = ref<number | null>(props.rotation ?? null)
const overlap = ref(15)
const layoutId = ref('')
const wantGrid = ref<{ rows: number; cols: number } | null>(initialGrid())
const framing = ref<Framing | null>(null)
const loading = ref(false)
const error = ref('')
const survey = ref(true)
const surveyFailed = ref(false)

function initialGrid(): { rows: number; cols: number } | null {
  if (props.cols && props.rows) return { rows: Math.max(1, props.rows), cols: Math.max(1, props.cols) }
  if (props.panels && props.panels > 1) return { rows: 1, cols: props.panels }
  return null
}

let ctl: AbortController | undefined
let timer: ReturnType<typeof setTimeout> | undefined

async function load() {
  ctl?.abort()
  ctl = new AbortController()
  loading.value = true
  try {
    const o = props.object
    const f = await frame(
      {
        ra: o.ra,
        dec: o.dec,
        majorArcmin: o.majorArcmin || 0,
        minorArcmin: o.minorArcmin || o.majorArcmin || 0,
        pa: o.pa || 0,
        rotation: rotation.value ?? undefined,
        overlap: overlap.value,
        rows: wantGrid.value?.rows,
        cols: wantGrid.value?.cols,
      },
      ctl.signal,
    )
    framing.value = f
    error.value = ''
    if (rotation.value === null) rotation.value = Math.round(f.rotation)
    const ids = f.options.map((x) => x.id)
    if (f.chosen && wantGrid.value) {
      layoutId.value = f.chosen.id
      wantGrid.value = null
    } else if (!ids.includes(layoutId.value)) {
      layoutId.value = f.options.find((x) => x.recommended)?.id ?? ids[0] ?? ''
    }
  } catch (e) {
    if ((e as Error).name !== 'AbortError') error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function schedule() {
  if (timer) clearTimeout(timer)
  timer = setTimeout(load, 150)
}

watch(() => `${props.object.id}|${props.object.ra}|${props.object.dec}`, () => {
  rotation.value = props.rotation ?? null
  wantGrid.value = initialGrid()
  layoutId.value = ''
  surveyFailed.value = false
  schedule()
}, { immediate: true })
watch([rotation, overlap], schedule)
onUnmounted(() => {
  ctl?.abort()
  if (timer) clearTimeout(timer)
})

const chosen = computed<FramingOption | null>(() => {
  const f = framing.value
  if (!f) return null
  return f.options.find((x) => x.id === layoutId.value) ?? f.options[0] ?? null
})

const shown = chosen

const custom = ref({ cols: props.cols ?? 2, rows: props.rows ?? 1 })

function useCustom() {
  wantGrid.value = { rows: Math.min(8, Math.max(1, custom.value.rows)), cols: Math.min(8, Math.max(1, custom.value.cols)) }
  load()
}

watch(chosen, (c) => {
  if (!c || rotation.value === null) return
  emit('update:plan', {
    rotation: rotation.value,
    overlap: overlap.value,
    layoutName: c.name,
    layoutId: c.id,
    cols: c.cols,
    rows: c.rows,
    panels: c.panels.map((p) => ({ raHours: (((p.centre.ra % 360) + 360) % 360) / 15, dec: p.centre.dec, rotation: Math.round(p.rotationDeg * 10) / 10 })),
  })
})

const centre = computed<SkyPoint>(() => ({ ra: props.object.ra, dec: props.object.dec }))

const view = computed(() => {
  const c = shown.value
  const rig = framing.value?.rig
  const pts: [number, number][] = []
  if (c) for (const p of c.panels) for (const v of p.footprint) pts.push(project(centre.value, v))
  const major = (props.object.majorArcmin || 0) / 60
  let ext = Math.max(major * 1.15, rig ? rig.widthDeg : 3.32)
  for (const [x, y] of pts) ext = Math.max(ext, Math.abs(x) * 2 * 1.1, Math.abs(y) * 2 * 1.1 * (W / H))
  const scale = W / ext
  return { scale, fovDeg: ext }
})

function toSvg(p: SkyPoint): [number, number] {
  const [xi, eta] = project(centre.value, p)
  return [W / 2 - xi * view.value.scale, H / 2 - eta * view.value.scale]
}

const polys = computed(() =>
  (shown.value?.panels ?? []).map((p) => {
    const corners = p.footprint.map(toSvg)
    const [cx, cy] = toSvg(p.centre)
    return { n: p.n, points: corners.map((c) => c.map((v) => v.toFixed(1)).join(',')).join(' '), cx, cy, covers: p.covers }
  }),
)

const outline = computed(() => {
  const s = view.value.scale
  const maj = ((props.object.majorArcmin || 0) / 60) * s
  const min = ((props.object.minorArcmin || props.object.majorArcmin || 0) / 60) * s
  return { rx: Math.max(3, min / 2), ry: Math.max(3, maj / 2), pa: props.object.pa || 0 }
})

const surveyUrl = computed(() => {
  const q = new URLSearchParams({
    hips: 'CDS/P/DSS2/color',
    width: String(W),
    height: String(H),
    fov: view.value.fovDeg.toFixed(3),
    projection: 'TAN',
    coordsys: 'icrs',
    ra: props.object.ra.toFixed(5),
    dec: props.object.dec.toFixed(5),
    rotation_angle: '0',
    format: 'jpg',
  })
  return 'https://alasky.cds.unistra.fr/hips-image-services/hips2fits?' + q.toString()
})

const panelCount = computed(() => shown.value?.panels.length ?? 0)
const coverText = computed(() => (shown.value ? Math.round(shown.value.coverage * 100) + '% of the outline' : ''))
const options = computed(() => framing.value?.options ?? [])
const rig = computed(() => framing.value?.rig)

function useSuggested() {
  if (framing.value) rotation.value = Math.round(framing.value.suggestedRotation)
}
</script>

<template>
  <div class="planner">
    <div class="spread">
      <span class="xsmall muted">RA {{ (object.ra / 15).toFixed(3) }} h · Dec {{ object.dec.toFixed(2) }}°{{ object.majorArcmin ? ` · ${object.majorArcmin.toFixed(0)}′ × ${(object.minorArcmin || object.majorArcmin).toFixed(0)}′ at ${object.pa || 0}°` : '' }}</span>
      <label class="row xsmall muted"><input v-model="survey" type="checkbox" /> Survey image</label>
    </div>
    <div class="row" style="gap: 1.5rem; align-items: flex-start">
      <svg :viewBox="`0 0 ${W} ${H}`" role="img" :aria-label="`${panelCount} ${panelCount === 1 ? 'frame' : 'panels'} over ${object.name || object.id}, north up, east left`" class="sky">
        <rect x="0" y="0" :width="W" :height="H" fill="var(--sky)" />
        <image v-if="survey && !surveyFailed" :href="surveyUrl" x="0" y="0" :width="W" :height="H" preserveAspectRatio="none" opacity="0.85" @error="surveyFailed = true" />
        <ellipse :cx="W / 2" :cy="H / 2" :rx="outline.rx" :ry="outline.ry" :transform="`rotate(${-outline.pa} ${W / 2} ${H / 2})`" fill="var(--neb)" fill-opacity="0.18" stroke="var(--neb)" stroke-dasharray="4 3" />
        <g v-for="p in polys" :key="p.n">
          <polygon :points="p.points" fill="#f4f4f8" fill-opacity="0.07" stroke="#f4f4f8" stroke-width="1.5" />
          <text v-if="panelCount > 1" :x="p.cx" :y="p.cy + 5" font-size="13" font-weight="600" fill="#f4f4f8" text-anchor="middle">{{ p.n }}</text>
        </g>
        <g :transform="`translate(${W - 30} 60)`" stroke="#f4f4f8" stroke-width="1.5" fill="none"><path d="M0 0v-30M0 0h-30" /></g>
        <text :x="W - 34" y="24" font-size="11" fill="#f4f4f8">N</text>
        <text :x="W - 72" y="64" font-size="11" fill="#f4f4f8">E</text>
        <text x="16" :y="H - 12" font-size="11" fill="#f4f4f8">{{ panelCount }} {{ panelCount === 1 ? 'frame' : 'panels' }}{{ coverText ? ' · covers ' + coverText : '' }}{{ rig ? ` · one frame is ${rig.widthDeg.toFixed(2)}° × ${rig.heightDeg.toFixed(2)}°` : '' }}</text>
      </svg>
      <div class="side">
        <label class="field small">
          <span class="spread"><span>Rotation</span><span class="num">{{ rotation ?? '…' }}°</span></span>
          <input v-model.number="rotation" type="range" min="0" max="179" :disabled="rotation === null" />
        </label>
        <button v-if="framing && rotation !== Math.round(framing.suggestedRotation)" type="button" class="btn link xsmall" style="align-self: flex-start" @click="useSuggested">
          Use {{ Math.round(framing.suggestedRotation) }}°, the fewest panels
        </button>
        <label class="field small">
          <span class="spread"><span>Panel overlap</span><span class="num">{{ overlap }}%</span></span>
          <input v-model.number="overlap" type="range" min="5" max="30" />
        </label>
        <template v-if="step === 'frame'">
          <div class="facts">
            <div class="fact"><span class="xsmall muted">Field of view</span><span class="num">{{ rig ? `${rig.widthDeg.toFixed(2)}° × ${rig.heightDeg.toFixed(2)}°` : '3.32° × 2.22°' }}</span></div>
            <div class="fact"><span class="xsmall muted">Scale</span><span class="num">{{ rig ? rig.scaleArcsec.toFixed(3) : '1.915' }}″/px</span></div>
            <div class="fact wide"><span class="xsmall muted">Rig</span><span>405 mm f/5.4 · ASI2600MM Pro</span></div>
          </div>
          <p class="small muted" style="margin: 0">{{ chosen && chosen.panels.length > 1 ? `At this rotation it needs ${chosen.name.toLowerCase()}; pick another layout on the next step.` : 'It fits one frame at this rotation.' }}</p>
        </template>
        <p v-if="error" class="small" style="margin: 0; color: var(--bad)">{{ error }}</p>
        <p v-else-if="loading && !framing" class="small muted" style="margin: 0">Working out layouts…</p>
      </div>
    </div>
    <fieldset v-if="step === 'mosaic'" class="layouts">
      <legend class="small" style="font-weight: 500; margin-bottom: 0.5rem">Layouts</legend>
      <label v-for="l in options" :key="l.id" class="layout" :class="{ on: l.id === layoutId }">
        <input v-model="layoutId" type="radio" name="layout" :value="l.id" />
        <span class="body">
          <span style="font-weight: 600">{{ l.name }}{{ l.recommended ? ' · recommended' : '' }}</span>
          <span class="muted">{{ l.detail }}</span>
          <span class="num">{{ l.cost }}</span>
        </span>
      </label>
      <div class="row small" style="gap: 0.5rem; grid-column: 1 / -1">
        <span class="muted">Or a grid of</span>
        <input v-model.number="custom.cols" class="input num" type="number" min="1" max="8" aria-label="Columns" style="width: 4.5rem" />
        <span class="muted">×</span>
        <input v-model.number="custom.rows" class="input num" type="number" min="1" max="8" aria-label="Rows" style="width: 4.5rem" />
        <button type="button" class="btn sm" @click="useCustom">Use this grid</button>
      </div>
      <p class="xsmall muted" style="margin: 0">
        Nights assume about 40% effective-to-open-shutter time and {{ framing ? framing.nightHours.toFixed(1) : '7' }} usable dark hours a night{{ framing && framing.bestMonths.length ? ' in ' + framing.bestMonths.join(', ') : ' in the object\'s best months' }}. Every panel gets the same goal, measured per panel; the weakest sets completion.
      </p>
    </fieldset>
  </div>
</template>

<style scoped>
.planner {
  display: flex;
  flex-direction: column;
  gap: 1rem;
  min-width: 0;
}
.sky {
  flex: 3 1 30rem;
  width: 100%;
  max-width: 100%;
  aspect-ratio: 8 / 5;
  border-radius: 0.5rem;
  background: var(--sky);
  display: block;
}
.side {
  flex: 1 1 16rem;
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  min-width: 0;
}
.facts {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0.5rem;
  font-size: 0.8125rem;
}
.fact {
  border: 1px solid var(--border);
  border-radius: 0.5rem;
  padding: 0.5rem 0.75rem;
  display: flex;
  flex-direction: column;
  font-weight: 600;
}
.fact.wide {
  grid-column: 1 / -1;
}
.layouts {
  border: 0;
  margin: 0;
  padding: 0;
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 16rem), 1fr));
  gap: 0.625rem;
}
.layouts legend,
.layouts p {
  grid-column: 1 / -1;
}
.layout {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 0.75rem;
  align-items: center;
  border: 1px solid var(--border);
  border-radius: 0.625rem;
  padding: 0.75rem;
  cursor: pointer;
  font-size: 0.8125rem;
}
.layout.on {
  border-color: var(--foreground);
  background: var(--secondary);
}
.layout .body {
  display: flex;
  flex-direction: column;
}
</style>
