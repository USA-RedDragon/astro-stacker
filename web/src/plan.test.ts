import { describe, expect, it } from 'vitest'
import type { CommandRecord } from './api/commands'
import type { PlanBlock, Preview, SchedProject, TonightSub } from './api/scheduler'
import {
  addChange,
  decDm,
  editsFor,
  filterColor,
  filterName,
  historyBadge,
  median,
  nightRange,
  pausePayload,
  pickTable,
  planDiff,
  planSummary,
  raHms,
  runFilters,
  runs,
  scale,
  signedHours,
  skipPayload,
  subChart,
  twilightBands,
  undoState,
  valueChoices,
} from './plan'

const t = (h: number, m = 0) => new Date(Date.UTC(2026, 9, 10, h, m)).toISOString()

const block = (p: Partial<PlanBlock>): PlanBlock => ({ start: t(1), end: t(2), wait: false, ...p })

const plan: Preview = {
  start: t(0),
  end: t(12),
  twilight: {
    sunset: t(0),
    civil_dusk: t(0, 30),
    nautical_dusk: t(1),
    astronomical_dusk: t(1, 30),
    astronomical_dawn: t(10),
    nautical_dawn: t(10, 30),
    civil_dawn: t(11),
    sunrise: t(11, 30),
  },
  blocks: [
    block({ start: t(1, 30), end: t(2, 30), target_id: 1, target_name: 'A', project_id: 9, project_name: 'P', filter: 'Ha', exposure_seconds: 300, count: 12, picked: true, scores: [{ rule: 'Priority', weight: 0.5, score: 1 }, { rule: 'Setting', weight: 0.5, score: 0.4 }], total: 0.7, runner_up: { target_name: 'B', total: 0.6 } }),
    block({ start: t(2, 30), end: t(4), target_id: 1, target_name: 'A', project_id: 9, filter: 'OIII', exposure_seconds: 300, count: 18, reason: 'sets' }),
    block({ start: t(4), end: t(4, 30), wait: true }),
    block({ start: t(4, 30), end: t(9), target_id: 2, target_name: 'B', project_id: 10, filter: 'L', exposure_seconds: 120, count: 100, picked: true, scores: [{ rule: 'Priority', weight: 0.5, score: 0.5 }, { rule: 'Mosaic', weight: 0, score: 0 }], total: 0.25, reason: 'complete' }),
  ],
}

describe('filters', () => {
  it('names and colours filters', () => {
    expect(filterName('Ha')).toBe('H-a')
    expect(filterName('O-III')).toBe('O-III')
    expect(filterName('L')).toBe('Luminance')
    expect(filterName('Weird')).toBe('Weird')
    expect(filterColor('SII')).toBe('var(--sii)')
    expect(filterColor('R')).toBe('var(--red)')
    expect(filterColor('Weird')).toBe('var(--muted-foreground)')
  })
})

describe('coordinates and numbers', () => {
  it('formats RA, Dec and durations', () => {
    expect(raHms(20.04)).toBe('20h 02m 24s')
    expect(decDm(35.98)).toBe('+35° 59′')
    expect(decDm(-5.5)).toBe('−05° 30′')
    expect(signedHours(4320)).toBe('+1.2 h')
    expect(signedHours(-1800)).toBe('−0.5 h')
    expect(median([3, 1, 2])).toBe(2)
    expect(median([1, 2, 3, 4])).toBe(2.5)
    expect(median([])).toBeNull()
  })
})

describe('runs', () => {
  it('merges consecutive blocks of a target and keeps the pick', () => {
    const r = runs(plan.blocks)
    expect(r).toHaveLength(3)
    expect(r[0].target_name).toBe('A')
    expect(r[0].subs).toBe(30)
    expect(r[0].reason).toBe('sets')
    expect(r[0].pick?.total).toBe(0.7)
    expect(runFilters(r[0])).toBe('H-a 300 s · O-III 300 s')
    expect(r[1].wait).toBe(true)
  })
})

describe('summary and pick table', () => {
  it('summarises the night', () => {
    const s = planSummary(plan)
    expect(s.darkSeconds).toBe(8.5 * 3600)
    expect(s.targets).toBe(2)
    expect(s.projects).toBe(2)
    expect(s.subs).toBe(130)
    expect(s.waits).toBe(1)
    expect(s.waitSeconds).toBe(1800)
  })

  it('builds rule columns from the scores present', () => {
    const { columns, rows } = pickTable(plan.blocks)
    expect(columns.map((c) => c.rule)).toEqual(['Priority', 'Setting', 'Mosaic'])
    expect(rows).toHaveLength(2)
    expect(rows[0].scores.Setting).toBeCloseTo(0.2)
    expect(rows[0].runner).toBe('B · 0.60')
    expect(rows[0].ends).toMatch(/^Sets at /)
    expect(rows[1].runner).toBe('—')
  })
})

describe('timeline geometry', () => {
  it('pads the night and lays out twilight bands', () => {
    const range = nightRange(plan)!
    expect(range[0]).toBe(Date.parse(t(0)) - 1800000)
    expect(range[1]).toBe(Date.parse(t(12)) + 1800000)
    const sc = scale(range[0], range[1], 0, 1000)
    const bands = twilightBands(plan.twilight, sc)
    expect(bands.map((b) => b.opacity)).toEqual([0.45, 0.32, 0.22, 0.1, 0.1, 0.22, 0.32, 0.45])
    expect(bands[0].x).toBe(0)
    expect(bands[bands.length - 1].x + bands[bands.length - 1].w).toBeCloseTo(1000)
  })

  it('skips missing twilight times', () => {
    const sc = scale(0, 10, 0, 10)
    expect(twilightBands({ sunset: null }, sc)).toEqual([{ x: 0, w: 10, opacity: 0.45 }])
    expect(nightRange({})).toBeNull()
  })
})

describe('what if', () => {
  const projects: SchedProject[] = [
    { id: 9, guid: 'g9', name: 'P', state: 1, priority: 1, minimumtime: 30, isMosaic: false, targets: [] },
    { id: 10, guid: 'g10', name: 'Q', state: 1, priority: 2, minimumtime: 60, isMosaic: true, targets: [] },
  ]

  it('replaces a change to the same field and groups edits per project', () => {
    let list = addChange([], { projectId: 9, field: 'priority', value: 2 })
    list = addChange(list, { projectId: 9, field: 'priority', value: 0 })
    list = addChange(list, { projectId: 9, field: 'minimumtime', value: 60 })
    list = addChange(list, { projectId: 10, field: 'state', value: 2 })
    expect(list).toHaveLength(3)
    const edits = editsFor(list, projects)
    expect(edits).toHaveLength(2)
    expect(edits[0]).toEqual({ entity: 'project', id: 9, guid: 'g9', name: 'P', changes: [{ field: 'priority', before: 1, after: 0 }, { field: 'minimumtime', before: 30, after: 60 }] })
    expect(edits[1].changes).toEqual([{ field: 'state', before: 1, after: 2 }])
    expect(valueChoices('priority', 1)).toEqual([2, 0])
  })

  it('diffs time per target', () => {
    const whatIf: Preview = { blocks: [block({ start: t(1, 30), end: t(9), target_id: 2, target_name: 'B' })] }
    const d = planDiff(plan, whatIf)
    expect(d.map((x) => [x.name, x.change])).toEqual([
      ['B', '+3.0 h'],
      ['A', 'no time'],
    ])
    expect(planDiff(plan, plan)).toEqual([{ name: 'A, B', before: 0, after: 0, change: 'no change' }])
  })
})

describe('sub chart', () => {
  const sub = (h: number, target: number, hfr: number, extra: Partial<TonightSub> = {}): TonightSub => ({
    id: h,
    time: t(h),
    project_id: 1,
    project: 'P',
    target_id: target,
    target: 'T' + target,
    filter: 'R',
    hfr,
    grading: 'accepted',
    ...extra,
  })

  it('breaks the line at target changes, marks rejects and draws medians', () => {
    const subs = [sub(1, 1, 2), sub(2, 1, 4, { verdict: 'low_score' }), sub(3, 1, 2.2), sub(4, 2, 2.5), sub(5, 2, 2.7)]
    const c = subChart(subs, (s) => s.hfr, 2, Date.parse(t(0)), Date.parse(t(6)), { x0: 56, x1: 1180, yTop: 20, yBottom: 190 })
    expect(c.paths).toHaveLength(2)
    expect(c.paths[1].current).toBe(true)
    expect(c.points.filter((p) => p.rejected)).toHaveLength(1)
    expect(c.changes).toEqual([{ x: expect.any(Number), label: 'T2' }])
    expect(c.medians.find((m) => m.current)?.value).toBeCloseTo(2.6)
    expect(c.medians.find((m) => !m.current)?.value).toBeCloseTo(2.1)
    expect(c.yTicks.length).toBeGreaterThan(2)
    expect(c.xTicks.map((x) => x.label)).toHaveLength(5)
  })
})

describe('commands', () => {
  it('builds skip and pause payloads', () => {
    const target = { project_id: 5, project_name: 'Sadr', target_id: 13, target_name: 'P15' }
    expect(skipPayload(target, false, 60)).toEqual({ scope: 'target', target_id: 13, target_name: 'P15', project_id: 5, project_name: 'Sadr', minutes: 60 })
    expect(skipPayload(target, true, 0).scope).toBe('project')
    expect(pausePayload('park', 'after', '')).toEqual({ mount: 'park', resume_after_minutes: 30 })
    expect(pausePayload('track', 'at', '03:00')).toEqual({ mount: 'track', resume_at: '03:00' })
    expect(pausePayload('track', 'manual', '03:00')).toEqual({ mount: 'track' })
  })

  it('labels history entries', () => {
    const rec = (p: Partial<CommandRecord>): CommandRecord => ({
      id: 'abcdef123456',
      kind: 'project.edit',
      payload: {},
      author: 'web',
      title: 'x',
      category: 'priority',
      destination: 'observatory',
      diffs: [],
      objects: [],
      status: 'applied',
      attempts: 0,
      created_at: t(1),
      updated_at: t(1),
      ...p,
    })
    expect(historyBadge(rec({ status: 'queued' }), '').tone).toBe('warn')
    expect(historyBadge(rec({ status: 'pending' }), '01:52').label).toBe('Applies at 01:52')
    expect(undoState(rec({ status: 'pending' }))).toMatchObject({ label: 'Cancel', cancel: true, enabled: true })
    expect(undoState(rec({ undone_by: 'ffff00001111' }))).toMatchObject({ label: 'Undone', enabled: false, note: 'Undone by change #ffff0000' })
    expect(undoState(rec({ status: 'conflict' })).enabled).toBe(false)
  })
})
