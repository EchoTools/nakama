<template>
  <aside :class="asideClass">
    <div v-if="mobile" class="flex items-center justify-between px-3 pb-3">
      <div class="text-sm font-semibold text-[#b9bbbe]">Menu</div>
      <button
        type="button"
        class="md:hidden inline-flex items-center justify-center w-9 h-9 rounded hover:bg-[#2f3136] text-[#b9bbbe] hover:text-white transition"
        aria-label="Close sidebar"
        @click="emit('close')"
      >
        ✕
      </button>
    </div>
    <nav class="flex flex-col gap-2">
      <button
        v-for="route in sidebarRoutes"
        :key="route.path"
        @click="navigateTo(route.path)"
        :class="[
          'text-left w-full px-3 py-2 rounded text-[#b9bbbe] focus:outline-none',
          router.currentRoute.value.path === route.path
            ? 'bg-[#5865f2] text-white font-bold'
            : 'hover:bg-[#2f3136] hover:text-white',
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

const props = defineProps({
  mobile: { type: Boolean, default: false },
});
const emit = defineEmits(['navigate', 'close']);

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
  emit('navigate');
};

const asideClass = computed(() => {
  return props.mobile
    ? 'w-full h-full bg-[#202225] pt-5 px-0 overflow-y-auto'
    : 'w-[220px] h-[calc(100vh-60px)] sticky top-[60px] self-start bg-[#202225] pt-5 px-3 overflow-y-auto';
});
</script>
