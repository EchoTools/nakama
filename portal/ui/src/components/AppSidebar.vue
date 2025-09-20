<template>
  <aside class="w-80 min-h-full bg-transparent pt-6 px-4">
    <nav class="flex flex-col gap-2">
      <button
        v-for="route in sidebarRoutes"
        :key="route.path"
        @click="navigateTo(route.path)"
        :class="[
          'text-left px-5 py-3 rounded-sm text-white font-medium focus:outline-none',
          router.currentRoute.value.path === route.path ? 'bg-[#393c4b]' : 'hover:bg-[#393c4b]',
        ]"
      >
        {{ route.meta?.title || route.name || route.path }}
      </button>
    </nav>
  </aside>
</template>

<script setup>
import { useRouter } from 'vue-router';
import { computed } from 'vue';

const router = useRouter();
// Only show routes that have meta.sidebar !== false
const sidebarRoutes = computed(() => {
  const routes = router.options.routes.filter((r) => r.meta?.sidebar !== false);
  // Add Player Lookup if not present
  if (!routes.find((r) => r.name === 'PlayerLookup')) {
    routes.push({
      path: '/player-lookup',
      name: 'PlayerLookup',
      meta: { title: 'Player Lookup' },
    });
  }
  return routes;
});

const navigateTo = (path) => {
  router.push(path);
};
</script>
