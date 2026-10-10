const KEY = 'theme'

export function initTheme() {
  let t: string | null = null
  try {
    t = localStorage.getItem(KEY)
  } catch {
    t = null
  }
  document.documentElement.setAttribute('data-theme', t === 'light' ? 'light' : 'dark')
}

export function toggleTheme() {
  const r = document.documentElement
  const next = r.getAttribute('data-theme') === 'light' ? 'dark' : 'light'
  r.setAttribute('data-theme', next)
  try {
    localStorage.setItem(KEY, next)
  } catch {
    return
  }
}
