<script setup>
import AppHeader from './components/AppHeader.vue';
import AppSidebar from './components/AppSidebar.vue';
import { ref, onMounted, computed } from 'vue';
import { useRouter } from 'vue-router'; // Import useRouter
import { useAvatar } from './composables/useAvatar.js';
import { useDiscordOAuth } from './composables/useDiscordOAuth.js';

const API_BASE = import.meta.env.VITE_NAKAMA_API_BASE;
const NAKAMA_SERVER_KEY = import.meta.env.VITE_NAKAMA_SERVER_KEY;
const DISCORD_CLIENT_ID = import.meta.env.VITE_DISCORD_CLIENT_ID;
const DISCORD_REDIRECT_URI = import.meta.env.VITE_DISCORD_REDIRECT_URI;

const token = ref(localStorage.getItem('jwt') || null);
const refreshToken = ref(localStorage.getItem('refreshToken') || null);
const status = ref('');
const user = ref(null);
const isLoggedIn = computed(() => !!token.value);
const router = useRouter(); // Get the router instance
const isCallbackRoute = computed(() => {
  const name = router.currentRoute.value?.name;
  return name === 'DiscordCallback';
});

const { getDefaultAvatar } = useAvatar();

// Validate environment variables
const envValid = computed(() => {
  return API_BASE && NAKAMA_SERVER_KEY && DISCORD_CLIENT_ID && DISCORD_REDIRECT_URI;
});

onMounted(async () => {
  if (!envValid.value) {
    status.value = 'Missing required environment variables. Please check your .env file.';
    return;
  }

  // Listen for login events from callback components
  window.addEventListener('userLoggedIn', handleUserLogin);

  // If user is already logged in, fetch their profile; otherwise show login prompt
  if (token.value) {
    await fetchUserProfile();
  } else if (!isCallbackRoute.value) {
    status.value = 'Please log in to continue.';
  }
});

async function fetchUserProfile() {
  if (!token.value) return;
  try {
    let response = await fetch(`${API_BASE}/account`, {
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${token.value}`,
      },
    });

    if (response.ok) {
      const userData = await response.json();
      user.value = {
        id: userData.custom_id || userData.user.id,
        username: userData.user.username,
        avatarUrl: userData.user.avatar_url || getDefaultAvatar(userData.user.username),
        displayName: userData.user.display_name || userData.user.username,
      };
      sessionStorage.removeItem('authRedirectInProgress');
      status.value = '';
      return;
    } else if (response.status === 401 && refreshToken.value) {
      // Try to refresh the token
      const refreshRes = await fetch(`${API_BASE}/account/session/refresh?unwrap`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Basic ${btoa(`${NAKAMA_SERVER_KEY}:`)}`,
        },
        body: JSON.stringify({ refresh_token: refreshToken.value }),
      });
      const newTokenData = await refreshRes.json();
      if (refreshRes.ok && newTokenData.token) {
        localStorage.setItem('jwt', newTokenData.token);
        token.value = newTokenData.token;
        // Retry fetching user profile with new token
        response = await fetch(`${API_BASE}/account`, {
          headers: {
            'Content-Type': 'application/json',
            Authorization: `Bearer ${token.value}`,
          },
        });
        if (response.ok) {
          const userData = await response.json();
          user.value = {
            id: userData.custom_id || userData.user.id,
            username: userData.user.username,
            avatarUrl: userData.user.avatar_url || getDefaultAvatar(userData.user.username),
            displayName: userData.user.display_name || userData.user.username,
          };
          sessionStorage.removeItem('authRedirectInProgress');
          status.value = '';
          return;
        }
      }
      // If refresh fails or new token doesn't work, log out
      logout();
      status.value = 'Session expired. Please log in again.';
      return;
    } else {
      throw new Error('Failed to fetch user profile');
    }
  } catch (error) {
    console.error('Failed to fetch user profile:', error);
    logout();
    status.value = 'Session expired. Please log in again.';
  }
}

function handleUserLogin(event) {
  console.log('User logged in:', event.detail);
  const { token: newToken, refreshToken: newRefreshToken, user: userData } = event.detail;
  token.value = newToken;
  refreshToken.value = newRefreshToken;
  user.value = userData;
  sessionStorage.removeItem('authRedirectInProgress');
  status.value = '';
}

const { loginDiscord } = useDiscordOAuth();
function login() {
  loginDiscord(window.location.pathname);
}

function logout() {
  localStorage.removeItem('jwt');
  localStorage.removeItem('refreshToken');
  token.value = null;
  refreshToken.value = null;
  user.value = null;
  sessionStorage.removeItem('authRedirectInProgress');
  status.value = 'You have been successfully logged out.';
}
</script>

<template>
  <!-- Applications Header Row -->
  <AppHeader :user="user" :onLogin="login" :onLogout="logout" />
  <div class="flex min-h-[calc(100vh-60px)]">
    <!-- Sidebar (hidden when logged out) -->
    <template v-if="isLoggedIn && user">
      <AppSidebar :router="router" />
    </template>
    <!-- Main content area -->
    <main class="flex-1 flex items-start justify-center">
      <template v-if="(isLoggedIn && user) || isCallbackRoute">
        <router-view />
      </template>
      <template v-else>
        <div
          class="max-w-lg w-full mx-auto mt-10 p-6 bg-gray-800 rounded-lg shadow text-gray-100 border border-gray-700"
        >
          <div class="flex items-center gap-3 mb-3">
            <img class="w-12 h-12 rounded-full border-2 border-purple-500" :src="getDefaultAvatar('')" alt="Avatar" />
            <h2 class="text-xl font-semibold">{{ status || 'Please log in to continue.' }}</h2>
          </div>
          <p class="text-gray-300 mb-4">Use Discord to authenticate and access the portal.</p>
          <button @click="login" class="px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white rounded">
            Login with Discord
          </button>
        </div>
      </template>
    </main>
  </div>
</template>
