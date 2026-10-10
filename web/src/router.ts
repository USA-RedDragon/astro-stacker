import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

export interface NavItem {
  name: string
  label: string
  note: string
}

export const navOperate: NavItem[] = [
  { name: 'now', label: 'Now', note: 'Current target, latest sub, skip and pause' },
  { name: 'tonight', label: 'Tonight', note: 'Timeline, why each pick, what-if' },
]

export const navPlan: NavItem[] = [
  { name: 'targets', label: 'Targets', note: 'All projects, bulk priority, open one' },
  { name: 'add', label: 'Add target', note: 'Find, frame, mosaic, goal' },
  { name: 'mosaics', label: 'Mosaics', note: 'Panels, seams, seasons, adoption' },
  { name: 'templates', label: 'Templates', note: 'Exposure sets and bulk edits' },
  { name: 'history', label: 'History', note: 'Every change, with undo' },
]

export const navDiscover: NavItem[] = [
  { name: 'catalogues', label: 'Catalogues', note: 'Completion and name matches' },
  { name: 'finder', label: 'Finder', note: 'Objects that suit your frame' },
  { name: 'collabs', label: 'Collabs', note: 'Starfront collaborations and fit' },
]

export const routes: RouteRecordRaw[] = [
  { path: '/', name: 'start', component: () => import('./pages/StartPage.vue'), meta: { title: 'Start' } },
  { path: '/now', name: 'now', component: () => import('./pages/NowPage.vue'), meta: { title: 'Now' } },
  { path: '/tonight', name: 'tonight', component: () => import('./pages/TonightPage.vue'), meta: { title: 'Tonight' } },
  { path: '/targets', name: 'targets', component: () => import('./pages/TargetsPage.vue'), meta: { title: 'Targets' } },
  {
    path: '/targets/:projectId',
    name: 'target',
    component: () => import('./pages/TargetDetailPage.vue'),
    props: true,
    meta: { title: 'Target', nav: 'targets' },
  },
  { path: '/add', name: 'add', component: () => import('./pages/AddTargetPage.vue'), meta: { title: 'Add target' } },
  { path: '/mosaics', name: 'mosaics', component: () => import('./pages/MosaicsPage.vue'), meta: { title: 'Mosaics' } },
  {
    path: '/mosaics/:projectId',
    name: 'mosaic',
    component: () => import('./pages/MosaicsPage.vue'),
    props: true,
    meta: { title: 'Mosaic', nav: 'mosaics' },
  },
  { path: '/templates', name: 'templates', component: () => import('./pages/TemplatesPage.vue'), meta: { title: 'Templates' } },
  { path: '/history', name: 'history', component: () => import('./pages/HistoryPage.vue'), meta: { title: 'History' } },
  { path: '/catalogues', name: 'catalogues', component: () => import('./pages/CataloguesPage.vue'), meta: { title: 'Catalogues' } },
  { path: '/finder', name: 'finder', component: () => import('./pages/FinderPage.vue'), meta: { title: 'Finder' } },
  { path: '/collabs', name: 'collabs', component: () => import('./pages/CollabsPage.vue'), meta: { title: 'Collabs' } },
  { path: '/:rest(.*)*', name: 'notfound', component: () => import('./pages/NotFoundPage.vue'), meta: { title: 'Not found' } },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior: () => ({ top: 0 }),
})

router.afterEach((to) => {
  const t = to.meta.title as string | undefined
  document.title = t ? `${t} · Observatory scheduler` : 'Observatory scheduler'
})
