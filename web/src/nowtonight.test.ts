import { describe, expect, it } from 'vitest'
import type { Conditions, MoonNight, Preview, SchedProject, TonightSub } from './api/scheduler'
import { conditionPills, hfrLimitFor, moonLine, moonPhaseText, mosaicBalance, nightSpan, noMoonAvoidance, noSourceText, phaseName, powerView, weatherRows } from './nowtonight'

const t = (h: number, m = 0) => new Date(Date.UTC(2026, 9, 10, h, m)).toISOString()

describe('conditions', () => {
  it('turns weather metrics into rows', () => {
    const rows = weatherRows({ source: 'prometheus', connected: true, cloud_cover: 0, rain_rate: 0, wind_speed: 2, humidity: 58, temperature: 15, dew_point: 6 })
    expect(rows.map((r) => r.label)).toEqual(['Sky', 'Rain', 'Wind', 'Humidity', 'Dew point gap', 'Air'])
    expect(rows[0].value).toBe('Clear · 0% cloud')
    expect(rows[2].value).toBe('7 km/h')
    expect(rows[4].value).toBe('9 °C')
    expect(weatherRows({ source: 'prometheus', connected: false, humidity: 50 })).toEqual([])
    expect(weatherRows({ source: 'none' })).toEqual([])
  })

  it('explains a missing source', () => {
    expect(noSourceText({ source: 'none' }, 'weather')).toBe('No data source for weather.')
    expect(noSourceText({ source: 'error', error: 'timeout' }, 'UPS')).toBe('The UPS source did not answer: timeout.')
  })

  it('shows the power state without load or watts', () => {
    expect(powerView({ source: 'none', on_battery: false, low_battery: false })).toBeNull()
    const on = powerView({ source: 'prometheus', on_battery: false, low_battery: false, flags: ['OL'], charge: 100, model: 'CyberPower EC450G', shutdown_seconds: 60 })!
    expect(on.label).toBe('On line')
    expect(on.alert).toBe('')
    const ob = powerView({ source: 'prometheus', on_battery: true, low_battery: false, flags: ['OB'], charge: 90, on_battery_seconds: 23 * 60, shutdown_seconds: 3600, input_voltage: 0 })!
    expect(ob.label).toBe('On battery')
    expect(ob.alert).toBe('On battery 0:23 · shutdown in 0:37')
    expect(ob.voltage).toBe('Input 0 V')
  })

  it('builds the sync, mount and safety pills', () => {
    const c: Conditions = {
      at: t(1),
      weather: { source: 'none' },
      safety: { source: 'prometheus', connected: true, safe: false },
      mount: { source: 'prometheus', connected: true, tracking: true, parked: false },
      power: { source: 'none', on_battery: false, low_battery: false },
      sync: { source: 'symmetricds', node: 'observatory', lag_seconds: 9, errors: 0 },
    }
    expect(conditionPills(c).map((p) => `${p.label} ${p.value}`)).toEqual(['Sync lag 9 s', 'Mount tracking', 'Safety monitor unsafe'])
    expect(conditionPills(null).map((p) => p.value)).toEqual(['no data source', 'no data source', 'no data source'])
    expect(conditionPills({ ...c, sync: { source: 'error', error: 'permission denied', errors: 0 } })[0].value).toBe('no access')
  })
})

describe('moon', () => {
  const m: MoonNight = {
    samples: [
      { t: t(1), alt: -20 },
      { t: t(6), alt: -5 },
      { t: t(11), alt: 10 },
    ],
    illumination: 0.01,
    age: 29.3,
    waxing: false,
    rises: [t(9)],
    sets: [],
    next_new: t(20),
    next_full: '2026-10-25T00:00:00Z',
  }
  it('describes rise and set inside the night', () => {
    expect(moonLine(m, Date.parse(t(1)), Date.parse(t(11)))).toBe('Moon 1%, rises 04:00. No moon avoidance tonight.')
    expect(moonLine(m, Date.parse(t(1)), Date.parse(t(7)))).toBe('Moon 1%, below the horizon all night. No moon avoidance tonight.')
  })
  it('says when moon avoidance is off', () => {
    const bright = { ...m, illumination: 0.4 }
    expect(noMoonAvoidance(bright, Date.parse(t(1)), Date.parse(t(11)))).toBe(false)
    expect(noMoonAvoidance(bright, Date.parse(t(1)), Date.parse(t(7)))).toBe(true)
    expect(noMoonAvoidance(m, Date.parse(t(1)), Date.parse(t(11)))).toBe(true)
    expect(noMoonAvoidance(null, 0, 1)).toBe(false)
    expect(moonLine(bright, Date.parse(t(1)), Date.parse(t(11)))).toBe('Moon 40%, rises 04:00.')
    expect(moonPhaseText(m, true)).toBe('1% lit · new moon Sat 10 Oct · no moon avoidance tonight')
  })
  it('names the phase', () => {
    expect(phaseName(7.4)).toBe('first quarter')
    expect(phaseName(25)).toBe('waning crescent')
    expect(moonPhaseText(m)).toBe('1% lit · new moon Sat 10 Oct')
    expect(moonPhaseText({ ...m, age: 25, illumination: 0.2 })).toBe('20% lit · waning crescent · new moon Sat 10 Oct')
  })
  it('labels the night', () => {
    expect(nightSpan(new Date(t(17)))).toBe('10–11 Oct')
    expect(nightSpan(new Date(Date.UTC(2026, 9, 31, 17)))).toBe('31 Oct–1 Nov')
  })
})

describe('reject line', () => {
  const subs = [{ target_id: 5, filter: 'Red' }, { target_id: 5, filter: 'H-a' }] as TonightSub[]
  const limits = [
    { target_id: 5, filter: 'Red', mean: 2, sd: 0.1, samples: 10, limit: 2.4 },
    { target_id: 5, filter: 'H-a', mean: 1.8, sd: 0.1, samples: 10, limit: 2.2 },
  ]
  it('uses the current filter, else the last sub', () => {
    expect(hfrLimitFor(limits, subs, 5)?.limit).toBe(2.2)
    expect(hfrLimitFor(limits, subs, 5, 'Red')?.limit).toBe(2.4)
    expect(hfrLimitFor(limits, subs, 6)).toBeNull()
    expect(hfrLimitFor([], subs, 5)).toBeNull()
  })
})

describe('mosaic balance', () => {
  const panels = [1, 2, 3, 4].map((i) => ({ id: i, guid: 'g' + i, name: 'P' + i, active: true, ra: 0, dec: 0, rotation: 0, plans: [] }))
  const projects: SchedProject[] = [{ id: 9, guid: 'm9', name: 'Sadr', state: 1, priority: 1, minimumtime: 30, isMosaic: true, targets: panels }]
  const block = (id: number, h0: number, h1: number, weight = 0) => ({
    start: t(h0),
    end: t(h1),
    wait: false,
    project_id: 9,
    target_id: id,
    target_name: 'P' + id,
    scores: [{ rule: 'Mosaic Completion', weight, score: 0 }],
  })
  const current: Preview = { blocks: [block(1, 1, 3), block(2, 3, 5)] }
  it('warns when one panel takes the night', () => {
    const whatIf: Preview = { blocks: [block(1, 1, 7)] }
    const w = mosaicBalance(current, whatIf, projects)
    expect(w).toHaveLength(1)
    expect(w[0].text).toBe('One Sadr panel would get 6.0 h while 3 of its 4 panels get none. Panel balancing is off for this mosaic (its rule weight is 0).')
  })
  it('stays quiet when time is spread', () => {
    expect(mosaicBalance(current, { blocks: [block(1, 1, 3), block(2, 3, 5), block(3, 5, 7)] }, projects)).toEqual([])
    expect(mosaicBalance(current, null, projects)).toEqual([])
  })
  it('drops the balancing note when a weight is set', () => {
    const w = mosaicBalance(current, { blocks: [block(1, 1, 7, 0.75)] }, projects)
    expect(w[0].balancingOff).toBe(false)
  })
})
