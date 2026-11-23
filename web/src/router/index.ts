import { createRouter, createWebHistory } from 'vue-router'

export const navItems = [
  { name: 'overview', path: '/overview', title: 'Overview', icon: 'mdi-view-dashboard-outline' },
  { name: 'accounts', path: '/accounts', title: 'Accounts', icon: 'mdi-account-multiple-outline' },
  { name: 'destinations', path: '/destinations', title: 'Destinations', icon: 'mdi-server-outline' },
  { name: 'sync', path: '/sync', title: 'Sync', icon: 'mdi-sync' },
  { name: 'history', path: '/history', title: 'History', icon: 'mdi-history' },
  { name: 'failures', path: '/failures', title: 'Failures', icon: 'mdi-alert-circle-outline' },
  { name: 'system', path: '/system', title: 'System', icon: 'mdi-heart-pulse' },
] as const

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/overview' },
    { path: '/overview', name: 'overview', component: () => import('../views/OverviewView.vue') },
    { path: '/accounts', name: 'accounts', component: () => import('../views/AccountsView.vue') },
    { path: '/accounts/:id', name: 'account', component: () => import('../views/AccountDetailView.vue') },
    { path: '/destinations', name: 'destinations', component: () => import('../views/DestinationsView.vue') },
    { path: '/sync', name: 'sync', component: () => import('../views/SyncView.vue') },
    { path: '/history', name: 'history', component: () => import('../views/HistoryView.vue') },
    { path: '/history/:id', name: 'session', component: () => import('../views/SessionDetailView.vue') },
    { path: '/failures', name: 'failures', component: () => import('../views/FailuresView.vue') },
    { path: '/system', name: 'system', component: () => import('../views/SystemView.vue') },
  ],
})

export default router
