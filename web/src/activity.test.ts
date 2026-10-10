import { describe, expect, it } from 'vitest'
import { activityLabel, activityLine, applyPhrase, liveExposureEnd } from './activity'
import { hm } from './format'

const t = (h: number, m = 0, s = 0) => new Date(Date.UTC(2026, 9, 10, h, m, s)).toISOString()
const now = new Date(t(2, 44, 32)).getTime()
const exposing = {
  state: 'imaging',
  activity: 'exposing',
  exposure: { filter: 'Ha', seconds: 300, started_at: t(2, 44, 21), ends_at: t(2, 49, 21), number: 1 },
}

describe('liveExposureEnd', () => {
  it('gives the end only while the exposure is still running', () => {
    expect(liveExposureEnd(exposing, now)).toBe(t(2, 49, 21))
    expect(liveExposureEnd(exposing, new Date(t(2, 49, 30)).getTime())).toBe('')
    expect(liveExposureEnd({ state: 'imaging', exposure: null }, now)).toBe('')
  })
})

describe('applyPhrase', () => {
  it('uses the plugin time while it is still ahead', () => {
    expect(applyPhrase(exposing, t(2, 49, 21), now)).toBe('at ' + hm(t(2, 49, 21)))
  })

  it('ignores a stale apply time and follows the live exposure', () => {
    expect(applyPhrase(exposing, t(2, 44, 11), now)).toBe('at ' + hm(t(2, 49, 21)))
  })

  it('says after the next exposure while slewing or focusing', () => {
    expect(applyPhrase({ state: 'imaging', activity: 'focusing', exposure: null }, null, now)).toBe('after the next exposure ends')
    expect(applyPhrase({ state: 'imaging', activity: 'slewing' }, t(2, 44, 11), now)).toBe('after the next exposure ends')
    expect(applyPhrase({ state: 'imaging' }, undefined, now)).toBe('after the next exposure ends')
  })

  it('says once saved while downloading, saving or dithering', () => {
    for (const activity of ['downloading', 'saving', 'dithering']) {
      expect(applyPhrase({ state: 'imaging', activity }, null, now)).toBe('once this exposure is saved')
    }
  })

  it('falls back to the next plan when nothing is imaging', () => {
    expect(applyPhrase({ state: 'waiting' }, null, now)).toBe('at the next plan')
    expect(applyPhrase({ state: 'imaging', paused: true }, null, now)).toBe('at the next plan')
  })
})

describe('activityLabel', () => {
  it('names the phase and the NINA step', () => {
    expect(activityLabel({ activity: 'focusing', activity_detail: 'AF After HFR Increase' })).toBe('Focusing · AF After HFR Increase')
    expect(activityLabel({ activity: 'slewing' })).toBe('Slewing')
    expect(activityLabel({ activity: 'busy', activity_detail: 'Open Dome' })).toBe('Open Dome')
    expect(activityLabel({ activity: 'busy' })).toBe('Busy')
    expect(activityLabel({ activity: 'something_new' })).toBe('something new')
    expect(activityLabel({})).toBe('')
  })

  it('adds how long the phase has run', () => {
    expect(activityLine({ activity: 'downloading', activity_since: t(2, 44, 2) }, now)).toBe('Downloading the exposure · 0:30')
  })
})
