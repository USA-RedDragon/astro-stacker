import { reactive } from 'vue'
import { ApiError } from './api/client'
import { onEvent } from './api/events'
import {
  cancelCommand,
  isWaiting,
  listCommands,
  undoCommand,
  type CommandRecord,
} from './api/commands'
import { getSchedulerStatus, type SchedulerStatus } from './api/scheduler'
import { applyPhrase, liveExposureEnd } from './activity'
import { hm } from './format'

export interface Toast {
  text: string
  sub?: string
  undoId?: string
  tone?: 'ok' | 'warn' | 'bad'
  at: number
}

export const shell = reactive({
  scheduler: { reachable: 'unknown' } as SchedulerStatus,
  waiting: [] as CommandRecord[],
  toast: null as Toast | null,
  statusOpen: false,
  now: Date.now(),
})

let toastTimer: ReturnType<typeof setTimeout> | undefined

export function showToast(t: Omit<Toast, 'at'>, ms = 8000) {
  shell.toast = { ...t, at: Date.now() }
  if (toastTimer) clearTimeout(toastTimer)
  toastTimer = setTimeout(() => (shell.toast = null), ms)
}

export function closeToast() {
  shell.toast = null
}

export function exposureEnd(): string {
  return hm(liveExposureEnd(shell.scheduler, shell.now))
}

export function whenApplies(r?: Pick<CommandRecord, 'applies_at'>): string {
  return applyPhrase(shell.scheduler, r?.applies_at, shell.now)
}

function pendingSub(when: string): string {
  return when.startsWith('at ') && exposureEnd() ? `Applies ${when}, when the current exposure ends.` : `Applies ${when}.`
}

export function queuedText(): string {
  switch (shell.scheduler.reachable) {
    case 'online':
      return 'Sent via the backup queue because the scheduler API did not answer in time. It applies when the current exposure ends.'
    case 'unconfigured':
      return 'Sent via the database queue, since the scheduler API is not set up. It applies when the PC picks it up.'
    case 'offline':
      return "The PC is unreachable, so it's queued and goes out when the PC answers."
    default:
      return 'Sent via the backup queue. It applies when the PC picks it up.'
  }
}

export function queuedShort(): string {
  return shell.scheduler.reachable === 'offline' ? 'queued offline' : 'sent via the backup queue'
}

export function describeOutcome(r: CommandRecord): { sub: string; tone?: Toast['tone'] } {
  switch (r.status) {
    case 'saved':
      return { sub: 'Saved in the app. Nothing changes in the scheduler.' }
    case 'queued':
      return { sub: queuedText(), tone: shell.scheduler.reachable === 'offline' ? 'warn' : undefined }
    case 'pending':
      return { sub: pendingSub(whenApplies(r)) }
    case 'applied':
      return { sub: 'Applied by the scheduler.', tone: 'ok' }
    case 'cancelled':
      return { sub: 'It never reached the observatory.' }
    case 'conflict':
      return { sub: r.message || 'It changed on the observatory since you loaded it. Nothing was applied.', tone: 'bad' }
    default:
      return { sub: r.message || 'The scheduler did not apply it.', tone: 'bad' }
  }
}

export function upsertWaiting(r: CommandRecord) {
  const i = shell.waiting.findIndex((x) => x.id === r.id)
  if (isWaiting(r.status)) {
    if (i >= 0) shell.waiting[i] = r
    else shell.waiting.push(r)
  } else if (i >= 0) {
    shell.waiting.splice(i, 1)
  }
}

export function notifyCommand(r: CommandRecord, text?: string) {
  upsertWaiting(r)
  const o = describeOutcome(r)
  const undoable = r.status !== 'cancelled' && r.status !== 'conflict' && r.status !== 'rejected' && r.status !== 'failed'
  showToast({ text: text ?? (r.undo_of ? r.title : 'Saved · ' + r.title), sub: o.sub, tone: o.tone, undoId: undoable ? r.id : undefined })
}

export function errorToast(e: unknown, text = 'That did not go through') {
  const sub = e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e)
  showToast({ text, sub, tone: 'bad' })
}

export async function undo(id: string) {
  try {
    const r = await undoCommand(id)
    if (r.id === id) {
      upsertWaiting(r)
      showToast({ text: 'Cancelled · ' + r.title, sub: 'It never reached the observatory.' })
    } else {
      upsertWaiting(r)
      const o = describeOutcome(r)
      showToast({ text: r.title.replace(/^Undo: /, 'Undone · '), sub: o.sub, tone: o.tone })
    }
    return r
  } catch (e) {
    errorToast(e, 'Could not undo')
    throw e
  }
}

export async function cancel(id: string) {
  try {
    const r = await cancelCommand(id)
    upsertWaiting(r)
    showToast({ text: 'Cancelled · ' + r.title, sub: 'It never reached the observatory.' })
    return r
  } catch (e) {
    errorToast(e, 'Could not cancel')
    throw e
  }
}

export async function refreshStatus() {
  try {
    shell.scheduler = await getSchedulerStatus()
  } catch {
    shell.scheduler = { ...shell.scheduler, reachable: shell.scheduler.reachable === 'unknown' ? 'unknown' : 'offline' }
  }
}

export async function refreshWaiting() {
  try {
    shell.waiting = await listCommands({ status: 'queued,pending' })
  } catch {
    return
  }
}

let started = false

export function startShell() {
  if (started) return
  started = true
  refreshStatus()
  refreshWaiting()
  setInterval(refreshStatus, 15000)
  setInterval(() => (shell.now = Date.now()), 1000)
  onEvent('scheduler', (e: { data?: SchedulerStatus }) => {
    if (e.data) shell.scheduler = e.data
  })
  onEvent('command', (e: { data?: CommandRecord }) => {
    const r = e.data
    if (!r) return
    const before = shell.waiting.find((x) => x.id === r.id)
    upsertWaiting(r)
    if (before && before.status !== r.status && r.status === 'applied') {
      showToast({ text: 'Applied · ' + r.title, sub: 'The scheduler re-plans from here.', tone: 'ok', undoId: r.id })
    } else if (before && (r.status === 'conflict' || r.status === 'rejected' || r.status === 'failed')) {
      showToast({ text: 'Not applied · ' + r.title, sub: describeOutcome(r).sub, tone: 'bad' })
    }
  })
}

export function indicator() {
  const s = shell.scheduler
  const pending = shell.waiting.filter((c) => c.status === 'pending')
  const queued = shell.waiting.filter((c) => c.status === 'queued')
  if (s.reachable === 'offline' || s.reachable === 'unconfigured') {
    const label = s.reachable === 'unconfigured' ? 'Scheduler not connected' : 'PC unreachable'
    return { label: queued.length ? `${label} · ${queued.length} queued` : label, dot: 'var(--bad)', tone: 'var(--bad)', bg: 'var(--bad-bg)' }
  }
  if (pending.length || queued.length) {
    const n = pending.length + queued.length
    const at = whenApplies(pending.length === 1 ? pending[0] : undefined)
    return {
      label: `${n} ${n === 1 ? 'change applies' : 'changes apply'} ${at}`,
      dot: 'var(--warn)',
      tone: 'var(--warn)',
      bg: 'var(--warn-bg)',
    }
  }
  if (s.reachable === 'unknown') return { label: 'Checking observatory', dot: 'var(--muted-foreground)', tone: 'var(--muted-foreground)', bg: 'transparent' }
  return { label: 'Observatory online', dot: 'var(--ok)', tone: 'var(--muted-foreground)', bg: 'transparent' }
}
