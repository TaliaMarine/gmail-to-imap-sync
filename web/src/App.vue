<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useDisplay, useTheme } from 'vuetify'
import { navItems } from './router'
import { snack } from './composables/useSnackbar'
import { api, type UserInfo } from './services/client'
import { useLiveStore } from './stores/live'

const theme = useTheme()
const { mdAndUp } = useDisplay()
const drawer = ref(true)
const rail = ref(localStorage.getItem('gas-rail') === '1')
const user = ref<UserInfo | null>(null)
const live = useLiveStore()

const isDark = computed(() => theme.global.current.value.dark)

function toggleTheme() {
  const next = isDark.value ? 'light' : 'dark'
  theme.change(next)
  localStorage.setItem('gas-theme', next)
}

function toggleRail() {
  rail.value = !rail.value
  localStorage.setItem('gas-rail', rail.value ? '1' : '0')
}

async function signOut() {
  await api.signOut()
  location.assign('/')
}

onMounted(async () => {
  try {
    user.value = await api.me()
    live.connect()
  } catch {
    // Not signed in: the server redirects browsers to the sign-in page.
  }
})
</script>

<template>
  <v-app>
    <v-app-bar flat density="comfortable" color="surface" class="gas-rule-b">
      <v-app-bar-nav-icon v-if="!mdAndUp" aria-label="Open navigation" @click="drawer = !drawer" />
      <router-link to="/" class="brand" aria-label="Gmail Archive Sync home">
        <img src="/favicon.svg" alt="" width="24" height="24" />
        <span>Gmail Archive Sync</span>
      </router-link>
      <v-spacer />
      <v-btn icon :aria-label="isDark ? 'Use light theme' : 'Use dark theme'" @click="toggleTheme">
        <v-icon :icon="isDark ? 'mdi-weather-sunny' : 'mdi-weather-night'" />
      </v-btn>
      <v-menu v-if="user">
        <template #activator="{ props }">
          <v-btn v-bind="props" prepend-icon="mdi-account-circle-outline">{{ user.name || user.preferred_username || user.email }}</v-btn>
        </template>
        <v-list density="compact">
          <v-list-item v-if="user.email" :subtitle="user.email" />
          <v-list-item v-if="user.can_sign_out" title="Sign out" prepend-icon="mdi-logout" @click="signOut" />
        </v-list>
      </v-menu>
    </v-app-bar>

    <v-navigation-drawer
      v-model="drawer"
      :permanent="mdAndUp"
      :rail="mdAndUp && rail"
      color="surface"
      class="gas-rule-r"
      width="232"
      aria-label="Main navigation"
    >
      <v-list nav density="comfortable">
        <v-list-item
          v-for="item in navItems"
          :key="item.name"
          :to="item.path"
          :prepend-icon="item.icon"
          :title="item.title"
          color="primary"
          rounded="md"
        />
      </v-list>
      <template v-if="mdAndUp" #append>
        <v-list nav density="comfortable">
          <v-list-item
            :prepend-icon="rail ? 'mdi-chevron-right' : 'mdi-chevron-left'"
            :title="rail ? 'Expand navigation' : 'Collapse navigation'"
            @click="toggleRail"
          />
        </v-list>
      </template>
    </v-navigation-drawer>

    <v-main>
      <v-alert v-if="live.status === 'reconnecting'" type="warning" variant="tonal" density="compact" class="ma-4 mb-0" role="status">
        Live updates paused. Reconnecting…
      </v-alert>
      <router-view />
    </v-main>

    <v-snackbar v-model="snack.show" :color="snack.color" :timeout="4000" location="bottom end" role="status">
      {{ snack.text }}
      <template #actions><v-btn variant="text" @click="snack.show = false">Dismiss</v-btn></template>
    </v-snackbar>
  </v-app>
</template>

<style scoped>
.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-inline-start: 12px;
  color: inherit;
  text-decoration: none;
  font-weight: 600;
}
</style>
