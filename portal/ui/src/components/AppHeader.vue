<template>
  <header class="flex justify-between items-center px-6 py-3 bg-gray-800 text-white shadow-md border-b border-gray-700">
    <div class="flex items-center space-x-3">
      <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f47e.svg" alt="Logo" class="w-8 h-8" />
      <span class="font-bold text-xl flex items-center">
        <span class="text-purple-500 text-2xl mr-1">.</span>
        portal
      </span>
    </div>
    <div class="flex-1 flex justify-center">
      <div class="relative w-full max-w-xs hidden">
        <span class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400">&#128269;</span>
        <input
          type="text"
          placeholder="Search"
          class="pl-10 pr-16 py-2 rounded-lg bg-gray-700 placeholder-gray-400 text-white focus:outline-none focus:ring-2 focus:ring-purple-500 w-full"
        />
        <span
          class="absolute right-3 top-1/2 -translate-y-1/2 flex space-x-1 bg-gray-700 px-2 py-0.5 rounded text-xs text-gray-300 border border-gray-600"
        >
          <span class="font-mono">Ctrl</span>
          <span class="font-mono">K</span>
        </span>
      </div>
    </div>

    <!-- User Authentication Section -->
    <div class="flex items-center space-x-4 ml-6">
      <span class="text-xl hidden" title="Dark Mode">&#127769;&#65039;</span>

      <!-- Authenticated User -->
      <div v-if="userState.profile" class="relative flex items-center space-x-3">
        <div class="relative">
          <img
            :src="userAvatar || defaultAvatar"
            alt="User Avatar"
            class="w-10 h-10 rounded-full border-2 border-purple-500 cursor-pointer hover:border-purple-400 transition select-none"
            draggable="false"
            @click="toggleDropdown"
          />

          <!-- Dropdown Menu -->
          <div
            v-if="showDropdown"
            class="absolute right-0 mt-1 w-32 p-0 bg-gray-700 rounded-sm shadow-md border border-gray-600 z-50"
            @click.stop
          >
            <div class="p-2 border-b border-gray-600">
              <div class="text-sm text-gray-300">Logged in as</div>
              <div class="text-sm font-medium text-white">{{ userState.profile.username }}</div>
            </div>
            <div class="py-1">
              <button
                @click="handleLogout"
                class="w-full text-left px-3 py-2 text-sm text-red-400 hover:bg-gray-600 hover:text-red-300 transition"
              >
                Logout
              </button>
            </div>
          </div>
        </div>
      </div>

      <!-- Guest User -->
      <div v-else class="flex items-center space-x-2">
        <button @click="handleLogin" class="px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white rounded transition">
          Login
        </button>
      </div>
    </div>
  </header>
</template>

<script setup>
import { ref, onMounted, onUnmounted, computed } from 'vue';
import { userState } from '../composables/useUserState.js';
import { useAvatar } from '../composables/useAvatar.js';

const DISCORD_CLIENT_ID = import.meta.env.VITE_DISCORD_CLIENT_ID;
const DISCORD_REDIRECT_URI = import.meta.env.VITE_DISCORD_REDIRECT_URI;

const status = ref('');
const { getDiscordAvatar, getDefaultAvatar } = useAvatar();
const defaultAvatar = computed(() => getDefaultAvatar(userState.profile?.username));
const userAvatar = computed(() => getDiscordAvatar(userState.profile));

const showDropdown = ref(false);

function toggleDropdown() {
  showDropdown.value = !showDropdown.value;
}

const DISCORD_OAUTH_URL = computed(() => {
  if (!DISCORD_CLIENT_ID || !DISCORD_REDIRECT_URI) return null;
  return `https://discord.com/api/oauth2/authorize?client_id=${DISCORD_CLIENT_ID}&redirect_uri=${encodeURIComponent(
    DISCORD_REDIRECT_URI
  )}&response_type=code&scope=identify`;
});

import { useDiscordOAuth } from '../composables/useDiscordOAuth.js';
const { loginDiscord } = useDiscordOAuth();
function handleLogin() {
  loginDiscord(window.location.pathname);
}

function handleLogout() {
  showDropdown.value = false;
  // Clear userState and tokens on logout
  userState.profile = null;
  userState.token = null;
  userState.refreshToken = null;
  localStorage.removeItem('jwt');
  localStorage.removeItem('refreshToken');
  // Optionally, redirect to home or login
  window.location.href = '/';
}

function closeDropdownOnClickOutside(event) {
  // Close dropdown if clicking outside of it
  if (!event.target.closest('.relative')) {
    showDropdown.value = false;
  }
}

onMounted(() => {
  document.addEventListener('click', closeDropdownOnClickOutside);
});

onUnmounted(() => {
  document.removeEventListener('click', closeDropdownOnClickOutside);
});
</script>
