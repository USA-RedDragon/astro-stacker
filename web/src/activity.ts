import type { SchedulerStatus } from './api/scheduler'
import { hm, mmss } from './format'

type Live = Pick<SchedulerStatus, 'state' | 'paused' | 'exposure' | 'activity' | 'activity_detail' | 'activity_since'>

const labels: Record<string, string> = {
  preparing: 'Getting ready for the next exposure',
  slewing: 'Slewing',
  centering: 'Centering',
  focusing: 'Focusing',
  guiding: 'Starting guiding',
  dithering: 'Dithering',
  meridian_flip: 'Meridian flip',
  switching_filter: 'Switching filter',
  waiting: 'Waiting',
  starting_exposure: 'Starting the exposure',
  exposing: 'Exposing',
  downloading: 'Downloading the exposure',
  saving: 'Saving the exposure',
}

const afterShutter = new Set(['downloading', 'saving', 'dithering'])

function at(v: string | null | undefined): number {
  if (!v) return NaN
  return new Date(v).getTime()
}

export function liveExposureEnd(s: Live, now: number): string {
  const end = at(s.exposure?.ends_at)
  return !isNaN(end) && end > now ? s.exposure?.ends_at ?? '' : ''
}

export function activityLabel(s: Live): string {
  const a = s.activity
  if (!a) return ''
  if (a === 'busy') return s.activity_detail || 'Busy'
  const base = labels[a] ?? a.replace(/_/g, ' ')
  return s.activity_detail && a !== 'exposing' ? `${base} · ${s.activity_detail}` : base
}

export function activityLine(s: Live, now: number): string {
  const label = activityLabel(s)
  if (!label) return ''
  const since = at(s.activity_since)
  return isNaN(since) ? label : `${label} · ${mmss((now - since) / 1000)}`
}

export function applyPhrase(s: Live, appliesAt: string | null | undefined, now: number): string {
  const when = at(appliesAt)
  if (!isNaN(when) && when > now) return 'at ' + hm(appliesAt)
  const end = liveExposureEnd(s, now)
  if (end) return 'at ' + hm(end)
  if (s.paused || s.state !== 'imaging') return 'at the next plan'
  if (s.activity && afterShutter.has(s.activity)) return 'once this exposure is saved'
  if (s.activity === 'exposing') return 'when this exposure ends'
  return 'after the next exposure ends'
}
