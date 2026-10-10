<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { draftApplySet, filterColor, getPlanning, uuid, type ApplySetDraft, type Snapshot, type Template } from '../api/planning'
import { submitCommand } from '../api/commands'
import { errorToast, notifyCommand } from '../shell'
import { onEvent } from '../api/events'
import { ApiError } from '../api/client'

const route = useRoute()
const snap = ref<Snapshot | null>(null)
const loadError = ref('')

async function load() {
  try {
    snap.value = await getPlanning()
    loadError.value = ''
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
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

const setId = ref('hoo')
const mode = ref<'add' | 'replace'>('add')
const picked = reactive<Record<number, boolean>>({})
const filterQ = ref('')
for (const id of String(route.query.targets ?? '').split(',')) {
  const n = Number(id)
  if (n > 0) picked[n] = true
}

const allTargets = computed(() =>
  (snap.value?.projects ?? []).flatMap((p) => p.targets.map((t) => ({ id: t.id, name: t.name, project: p.name, set: t.exposureSet, state: p.state }))),
)
const shownTargets = computed(() => {
  const q = filterQ.value.trim().toLowerCase()
  const list = allTargets.value.filter((t) => !q || t.name.toLowerCase().includes(q) || t.project.toLowerCase().includes(q))
  return list.sort((a, b) => Number(!!picked[b.id]) - Number(!!picked[a.id]))
})
const pickedIds = computed(() => allTargets.value.filter((t) => picked[t.id]).map((t) => t.id))

const draft = ref<ApplySetDraft | null>(null)
const draftError = ref('')
let draftTimer: ReturnType<typeof setTimeout> | undefined
watch(
  [setId, mode, pickedIds],
  () => {
    if (draftTimer) clearTimeout(draftTimer)
    draftTimer = setTimeout(refreshDraft, 200)
  },
  { deep: true },
)

async function refreshDraft() {
  if (!pickedIds.value.length) {
    draft.value = null
    draftError.value = ''
    return
  }
  try {
    draft.value = await draftApplySet({ setId: setId.value, mode: mode.value, targetIds: pickedIds.value })
    draftError.value = ''
  } catch (e) {
    draft.value = e instanceof ApiError && e.body && typeof e.body === 'object' && 'draft' in e.body ? ((e.body as { draft: ApplySetDraft }).draft ?? null) : null
    draftError.value = e instanceof Error ? e.message : String(e)
  }
}

function effectOf(id: number): string {
  if (!picked[id]) return '—'
  const e = draft.value?.effects.find((x) => x.targetId === id)
  return e ? e.effect : draftError.value ? '' : '…'
}

const changingCount = computed(() => draft.value?.effects.filter((e) => e.changes).length ?? 0)
const applyLabel = computed(() => {
  const set = snap.value?.sets.find((s) => s.id === setId.value)
  const n = changingCount.value
  return `Apply ${set?.name ?? 'set'} to ${n} ${n === 1 ? 'target' : 'targets'}`
})

async function applyTargets() {
  if (!draft.value?.payload) return
  try {
    const r = await submitCommand('exposureplan.applyset', draft.value.payload)
    notifyCommand(r)
    Object.keys(picked).forEach((k) => delete picked[Number(k)])
    await load()
  } catch (e) {
    errorToast(e)
  }
}

interface BulkField {
  id: string
  column: string
  label: string
  unit: string
  get: (t: Template) => number | null
}

const fields: BulkField[] = [
  { id: 'moonSep', column: 'moonavoidanceseparation', label: 'Moon avoidance separation', unit: '°', get: (t) => t.moonSeparation },
  { id: 'moonWidth', column: 'moonavoidancewidth', label: 'Moon avoidance width', unit: 'days', get: (t) => t.moonWidth },
  { id: 'humidity', column: 'maximumhumidity', label: 'Maximum humidity', unit: '%', get: (t) => t.maximumHumidity },
  { id: 'gain', column: 'gain', label: 'Gain', unit: '', get: (t) => t.gain },
  { id: 'offset', column: 'offset', label: 'Offset', unit: '', get: (t) => t.offset },
  { id: 'exp', column: 'defaultexposure', label: 'Default exposure', unit: 's', get: (t) => t.defaultExposure },
]

const tsel = reactive<Record<number, boolean>>({})
const fieldId = ref('moonSep')
const value = ref<number | null>(null)
const field = computed(() => fields.find((f) => f.id === fieldId.value)!)
const templates = computed(() => snap.value?.templates ?? [])
const selTemplates = computed(() => templates.value.filter((t) => tsel[t.id]))
const allChecked = computed(() => templates.value.length > 0 && templates.value.every((t) => tsel[t.id]))

function fmt(v: number | null, unit: string): string {
  if (v === null || v === undefined) return '—'
  return unit ? `${v}${unit === '°' || unit === '%' ? '' : ' '}${unit}` : String(v)
}

const preview = computed(() =>
  selTemplates.value.map((t) => {
    const before = field.value.get(t)
    const changes = value.value !== null && before !== value.value
    return { id: t.id, label: `${t.name} ${fmt(before, field.value.unit)}${changes ? ' → ' + fmt(value.value, field.value.unit) : ' · unchanged'}`, changes }
  }),
)
const reach = computed(() => {
  const ch = selTemplates.value.filter((t) => value.value !== null && field.value.get(t) !== value.value)
  const plans = ch.reduce((a, t) => a + t.usedByPlans, 0)
  const targets = ch.reduce((a, t) => a + t.usedByTargets, 0)
  return ch.length ? `Reaches ${plans} ${plans === 1 ? 'plan' : 'plans'} on ${targets} ${targets === 1 ? 'target' : 'targets'}.` : ''
})
const changingTemplates = computed(() => preview.value.filter((p) => p.changes).length)

function toggleAll(e: Event) {
  const v = (e.target as HTMLInputElement).checked
  templates.value.forEach((t) => (tsel[t.id] = v))
}

function editOne(t: Template) {
  Object.keys(tsel).forEach((k) => delete tsel[Number(k)])
  tsel[t.id] = true
  value.value = field.value.get(t)
}

async function applyBulk() {
  if (value.value === null) return
  const v = value.value
  const items = selTemplates.value
    .filter((t) => field.value.get(t) !== v)
    .map((t) => ({ id: t.id, guid: t.guid, name: t.name, changes: [{ field: field.value.column, before: field.value.get(t), after: v }] }))
  if (!items.length) return
  try {
    const r = await submitCommand('exposuretemplate.batchedit', { items })
    notifyCommand(r)
    await load()
  } catch (e) {
    errorToast(e)
  }
}

const clone = reactive({ open: false, source: null as Template | null, name: '', exp: 0, gain: 0 })

function openClone(t: Template) {
  clone.source = t
  clone.name = t.name + ' copy'
  clone.exp = t.defaultExposure
  clone.gain = t.gain ?? 0
  clone.open = true
}

const cloneNameTaken = computed(() => templates.value.some((t) => t.name.toLowerCase() === clone.name.trim().toLowerCase()))

async function createClone() {
  const s = clone.source
  if (!s || !clone.name.trim() || cloneNameTaken.value) return
  try {
    const r = await submitCommand('exposuretemplate.clone', {
      source_id: s.id,
      source_name: s.name,
      guid: uuid(),
      name: clone.name.trim(),
      defaultexposure: clone.exp,
      gain: clone.gain,
    })
    clone.open = false
    notifyCommand(r)
    await load()
  } catch (e) {
    errorToast(e)
  }
}

function moonText(t: Template): string {
  if (!t.moonEnabled) return 'Off'
  return `${t.moonSeparation}° / ${t.moonWidth} d${t.moonDown ? ' · moon-down' : ''}`
}
</script>

<template>
  <main class="page wide">
    <div class="page-head">
      <div>
        <h1>Templates</h1>
        <p class="lede">Apply an exposure set to many targets at once, or edit exposure templates together. A template change reaches every plan that uses it.</p>
      </div>
    </div>
    <p v-if="loadError" class="empty" style="color: var(--bad)">Could not load templates: {{ loadError }}</p>

    <section class="card" aria-labelledby="set-h">
      <div>
        <h2 id="set-h">Apply an exposure set to targets</h2>
        <p class="small muted" style="margin: 0.125rem 0 0">Pick a set, tick the targets, then apply. Selections made in Targets arrive here already ticked.</p>
      </div>
      <div role="radiogroup" aria-label="Exposure set" class="sets">
        <button
          v-for="s in snap?.sets ?? []"
          :key="s.id"
          type="button"
          role="radio"
          :aria-checked="setId === s.id"
          class="setcard"
          :class="{ on: setId === s.id }"
          @click="setId = s.id"
        >
          <span style="font-weight: 600; font-size: 0.8125rem">{{ s.name }}</span>
          <span class="xsmall muted">{{ s.items.map((i) => i.template + ' ' + i.exposure + ' s').join(' · ') }}</span>
        </button>
      </div>
      <fieldset class="row" style="border: 0; margin: 0; padding: 0; gap: 1rem">
        <legend class="small" style="font-weight: 500; margin-bottom: 0.375rem">Existing exposure plans</legend>
        <label class="row" style="font-size: 0.875rem"><input v-model="mode" type="radio" value="add" /> Keep them, add what's missing</label>
        <label class="row" style="font-size: 0.875rem"><input v-model="mode" type="radio" value="replace" /> Replace them with this set (others are turned off, counts kept)</label>
      </fieldset>
      <label class="field" style="max-width: 24rem">
        <span>Find targets</span>
        <input v-model="filterQ" class="input" type="search" placeholder="Name or project" />
      </label>
      <div class="scroll-x" style="border: 1px solid var(--border); border-radius: 0.5rem; max-height: 22rem; overflow-y: auto">
        <table class="grid">
          <thead>
            <tr><th style="width: 2rem"><span class="sr-only">Apply</span></th><th>Target</th><th>Current set</th><th>What happens</th></tr>
          </thead>
          <tbody>
            <tr v-for="t in shownTargets" :key="t.id" :style="{ background: picked[t.id] ? 'var(--sel)' : 'transparent' }">
              <td><input v-model="picked[t.id]" type="checkbox" :aria-label="'Apply to ' + t.name" /></td>
              <td style="font-weight: 600; white-space: nowrap">{{ t.name }}<span v-if="t.name !== t.project" class="xsmall muted" style="font-weight: 400"> · {{ t.project }}</span></td>
              <td class="muted" style="white-space: nowrap">{{ t.set }}</td>
              <td>{{ effectOf(t.id) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-if="draftError" class="small" style="margin: 0; color: var(--bad)">{{ draftError }}</p>
      <div class="row" style="justify-content: flex-end">
        <button type="button" class="btn" @click="Object.keys(picked).forEach((k) => delete picked[Number(k)])">Clear</button>
        <button type="button" class="btn primary" :disabled="!draft?.payload" @click="applyTargets">{{ applyLabel }}</button>
      </div>
    </section>

    <div role="region" aria-label="Bulk edit templates" class="bulk">
      <div class="row" style="align-items: flex-end; gap: 0.75rem">
        <div style="font-weight: 600; font-size: 0.9375rem; margin-right: auto; align-self: center">
          {{ selTemplates.length ? selTemplates.length + (selTemplates.length === 1 ? ' template selected' : ' templates selected') : 'Select templates below to edit them together' }}
        </div>
        <label class="field">
          <span>Set</span>
          <select v-model="fieldId" class="input">
            <option v-for="fo in fields" :key="fo.id" :value="fo.id">{{ fo.label }}</option>
          </select>
        </label>
        <label class="field">
          <span>To</span>
          <span class="row" style="flex-wrap: nowrap">
            <input v-model.number="value" class="input num" type="number" style="width: 5.5rem" />
            <span>{{ field.unit }}</span>
          </span>
        </label>
        <button type="button" class="btn" @click="Object.keys(tsel).forEach((k) => delete tsel[Number(k)])">Clear selection</button>
        <button type="button" class="btn primary" :disabled="!changingTemplates" @click="applyBulk">
          Apply to {{ changingTemplates }} {{ changingTemplates === 1 ? 'template' : 'templates' }}
        </button>
      </div>
      <div v-if="selTemplates.length" class="row small num">
        <span v-for="pv in preview" :key="pv.id" class="chip" :class="{ on: pv.changes }">{{ pv.label }}</span>
        <span class="muted">{{ reach }}</span>
      </div>
    </div>

    <section class="card" aria-labelledby="t-h">
      <h2 id="t-h">{{ templates.length }} exposure templates</h2>
      <div class="scroll-x" style="border: 1px solid var(--border); border-radius: 0.5rem">
        <table class="grid num" style="min-width: 56rem">
          <thead>
            <tr>
              <th><input type="checkbox" :checked="allChecked" aria-label="Select all templates" @change="toggleAll" /></th>
              <th>Name</th><th>Filter</th><th style="text-align: right">Exposure</th><th style="text-align: right">Gain</th><th>Twilight</th><th>Moon avoidance</th><th>Humidity</th><th style="text-align: right">Used by</th>
              <th><span class="sr-only">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="t in templates" :key="t.id" :style="{ background: tsel[t.id] ? 'var(--sel)' : 'transparent' }">
              <td><input v-model="tsel[t.id]" type="checkbox" :aria-label="'Select ' + t.name" /></td>
              <td style="font-weight: 600; white-space: nowrap">{{ t.name }}</td>
              <td style="white-space: nowrap"><span class="swatch" :style="{ background: filterColor(t.filter) }" />{{ t.filter }}</td>
              <td style="text-align: right">{{ t.defaultExposure }} s</td>
              <td style="text-align: right">{{ t.gain ?? '—' }}</td>
              <td style="white-space: nowrap">{{ t.twilight }}</td>
              <td style="white-space: nowrap">{{ moonText(t) }}</td>
              <td class="muted">{{ t.maximumHumidity ? t.maximumHumidity + '%' : '—' }}</td>
              <td style="text-align: right; white-space: nowrap">{{ t.usedByPlans }} plans · {{ t.usedByTargets }} targets</td>
              <td style="text-align: right; white-space: nowrap">
                <span class="row" style="flex-wrap: nowrap; justify-content: flex-end; gap: 0.25rem">
                  <button type="button" class="btn sm" @click="openClone(t)">Clone</button>
                  <button type="button" class="btn sm" @click="editOne(t)">Edit</button>
                </span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="xsmall muted" style="margin: 0">Moon avoidance is separation / width in days, with moon-down relaxing it where shown. Edit selects one template for the bar above.</p>
    </section>

    <div v-if="clone.open" class="overlay">
      <div class="scrim" aria-hidden="true" @click="clone.open = false" />
      <div role="dialog" aria-modal="true" aria-labelledby="clone-h" class="dialog">
        <h2 id="clone-h">Copy {{ clone.source?.name }}</h2>
        <div class="row" style="gap: 0.75rem; align-items: flex-end">
          <label class="field" style="flex: 2 1 10rem"><span>Name</span><input v-model="clone.name" class="input" type="text" /></label>
          <label class="field" style="flex: 1 1 6rem"><span>Default exposure, s</span><input v-model.number="clone.exp" class="input num" type="number" min="1" /></label>
          <label class="field" style="flex: 1 1 5rem"><span>Gain</span><input v-model.number="clone.gain" class="input num" type="number" min="0" /></label>
        </div>
        <p class="small muted">
          {{ cloneNameTaken ? 'A template with that name already exists.' : 'Everything else, moon avoidance and twilight included, is copied from ' + clone.source?.name + '. Undo removes the copy while no plan uses it.' }}
        </p>
        <div class="actions">
          <button type="button" class="btn" @click="clone.open = false">Cancel</button>
          <button type="button" class="btn primary" :disabled="cloneNameTaken || !clone.name.trim()" @click="createClone">Create copy</button>
        </div>
      </div>
    </div>
  </main>
</template>

<style scoped>
.sets {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 12rem), 1fr));
  gap: 0.5rem;
}
.setcard {
  text-align: left;
  border: 1px solid var(--border);
  border-radius: 0.625rem;
  padding: 0.625rem 0.75rem;
  background: transparent;
  display: flex;
  flex-direction: column;
  gap: 0.125rem;
}
.setcard.on {
  border-color: var(--foreground);
  background: var(--secondary);
}
.bulk {
  display: flex;
  flex-direction: column;
  gap: 0.875rem;
  border: 1px solid var(--foreground);
  border-radius: 14px;
  padding: 1.25rem;
  background: var(--card);
}
.chip {
  background: var(--secondary);
  border-radius: 0.375rem;
  padding: 0.25rem 0.625rem;
}
.chip.on {
  background: var(--info-bg);
  color: var(--info);
}
.swatch {
  display: inline-block;
  width: 0.625rem;
  height: 0.625rem;
  border-radius: 2px;
  margin-right: 0.375rem;
}
</style>
