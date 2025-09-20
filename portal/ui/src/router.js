import { createRouter, createWebHistory } from 'vue-router';

import Applications from './views/Applications.vue';
import Welcome from './views/Welcome.vue';
// Import OAuth callback components
import DiscordCallback from './views/auth/DiscordCallback.vue';

const routes = [
  {
    path: '/player-lookup',
    name: 'PlayerLookup',
    component: () => import('./views/PlayerLookup.vue'),
    meta: { title: 'Player Lookup' },
  },
  {
    path: '/',
    name: 'Welcome',
    component: Welcome,
    meta: { title: 'Welcome' },
  },
  {
    path: '/applications',
    name: 'Applications',
    component: Applications,
    meta: { title: 'Applications' },
  },
  // OAuth callback routes
  {
    path: '/auth/discord/callback',
    name: 'DiscordCallback',
    component: DiscordCallback,
    meta: { title: 'Discord Authentication', sidebar: false },
  },
  // Add more routes for other sections as needed
];

const router = createRouter({
  history: createWebHistory(),
  routes,
});

export default router;
