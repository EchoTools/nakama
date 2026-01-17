<script setup>
import AppHeader from './components/AppHeader.vue';
import AppSidebar from './components/AppSidebar.vue';
import { ref, onMounted, onUnmounted, computed } from 'vue';
import { useRouter } from 'vue-router'; // Import useRouter
import { useAvatar } from './composables/useAvatar.js';
import { useDiscordOAuth } from './composables/useDiscordOAuth.js';
import { userState } from './composables/useUserState.js';

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

const sidebarOpen = ref(false);
const showSidebar = computed(() => isLoggedIn.value && !!user.value);

const isMobile = ref(false);
function updateIsMobile() {
  try {
    isMobile.value = window.matchMedia('(max-width: 767px)').matches;
  } catch (_) {
    // Fallback: assume not mobile.
    isMobile.value = false;
  }
}

// Swipe gesture state.
let touchActive = false;
let touchStartX = 0;
let touchStartY = 0;
let touchCanOpen = false;
let touchCanClose = false;

const DRAWER_WIDTH_PX = 260;
const SWIPE_TRIGGER_PX = 60;
const VERTICAL_TOLERANCE_PX = 60;

function shouldIgnoreTouchTarget(target) {
  if (!(target instanceof Element)) return false;
  return !!target.closest('input, textarea, select, button, a');
}

function onTouchStart(e) {
  if (!isMobile.value) return;
  if (!e.touches || e.touches.length !== 1) return;
  if (shouldIgnoreTouchTarget(e.target)) return;

  const t = e.touches[0];
  touchActive = true;
  touchStartX = t.clientX;
  touchStartY = t.clientY;
  // User requested: allow swipe open from anywhere on screen.
  // Close should also work from anywhere when the drawer is open.
  touchCanOpen = !sidebarOpen.value;
  touchCanClose = sidebarOpen.value;
}

function onTouchMove(e) {
  if (!touchActive || !isMobile.value) return;
  if (!e.touches || e.touches.length !== 1) return;

  const t = e.touches[0];
  const dx = t.clientX - touchStartX;
  const dy = t.clientY - touchStartY;

  // Ignore mostly-vertical moves (scrolling).
  if (Math.abs(dy) > VERTICAL_TOLERANCE_PX) return;

  // Open: swipe right from left edge.
  if (touchCanOpen && dx >= SWIPE_TRIGGER_PX && Math.abs(dx) > Math.abs(dy) * 1.5) {
    sidebarOpen.value = true;
    touchActive = false;
    return;
  }

  // Close: swipe left starting inside the drawer.
  if (touchCanClose && dx <= -SWIPE_TRIGGER_PX && Math.abs(dx) > Math.abs(dy) * 1.5) {
    sidebarOpen.value = false;
    touchActive = false;
  }
}

function onTouchEnd() {
  touchActive = false;
  touchCanOpen = false;
  touchCanClose = false;
}

onMounted(() => {
  updateIsMobile();
  window.addEventListener('resize', updateIsMobile);
  window.addEventListener('touchstart', onTouchStart, { passive: true });
  window.addEventListener('touchmove', onTouchMove, { passive: true });
  window.addEventListener('touchend', onTouchEnd, { passive: true });
  window.addEventListener('touchcancel', onTouchEnd, { passive: true });
});

onUnmounted(() => {
  window.removeEventListener('resize', updateIsMobile);
  window.removeEventListener('touchstart', onTouchStart);
  window.removeEventListener('touchmove', onTouchMove);
  window.removeEventListener('touchend', onTouchEnd);
  window.removeEventListener('touchcancel', onTouchEnd);
});

router.afterEach(() => {
  // Always close the mobile drawer after navigation.
  sidebarOpen.value = false;
});

const { getDefaultAvatar, extractDiscordAvatarHash } = useAvatar();

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
        nakamaId: userData.user.id, // Nakama user ID for API calls
        id: userData.custom_id, // Discord ID (used by getDiscordAvatar)
        username: userData.user.username,
        avatar: extractDiscordAvatarHash(userData.user.avatar_url), // Avatar hash for Discord CDN
        avatarUrl: userData.user.avatar_url || getDefaultAvatar(userData.user.username),
        displayName: userData.user.display_name || userData.user.username,
      };

      // Update global userState for header component
      userState.profile = user.value;
      userState.token = token.value;
      userState.refreshToken = refreshToken.value;

      // Fetch user's group memberships to check permissions
      await fetchUserGroups(user.value.nakamaId);

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
            nakamaId: userData.user.id, // Nakama user ID for API calls
            id: userData.custom_id, // Discord ID (used by getDiscordAvatar)
            username: userData.user.username,
            avatar: extractDiscordAvatarHash(userData.user.avatar_url), // Avatar hash for Discord CDN
            avatarUrl: userData.user.avatar_url || getDefaultAvatar(userData.user.username),
            displayName: userData.user.display_name || userData.user.username,
          };

          // Update global userState for header component
          userState.profile = user.value;
          userState.token = token.value;
          userState.refreshToken = refreshToken.value;

          // Fetch user's group memberships to check permissions
          await fetchUserGroups(user.value.nakamaId);

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

async function fetchUserGroups(userId) {
  try {
    const response = await fetch(`${API_BASE}/user/${userId}/group?limit=100`, {
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${token.value}`,
      },
    });

    if (response.ok) {
      const data = await response.json();
      // Filter for system groups (lang_tag = 'system') and member/admin states
      const systemGroups = (data.user_groups || [])
        .filter((g) => g.group?.lang_tag === 'system' && g.state <= 2)
        .map((g) => g.group.name);
      userState.userGroups = systemGroups;
    }
  } catch (error) {
    console.error('Failed to fetch user groups:', error);
    userState.userGroups = [];
  }
}

async function handleUserLogin(event) {
  console.log('User logged in:', event.detail);
  const { token: newToken, refreshToken: newRefreshToken, user: userData } = event.detail;
  token.value = newToken;
  refreshToken.value = newRefreshToken;
  user.value = userData;
  // Update global userState for header component
  userState.profile = userData;
  userState.token = newToken;
  userState.refreshToken = newRefreshToken;

  // Fetch user's group memberships to check permissions
  await fetchUserGroups(userData.nakamaId);

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
  userState.userGroups = [];
  sessionStorage.removeItem('authRedirectInProgress');
  status.value = 'You have been successfully logged out.';
}
</script>

<template>
  <!-- Applications Header Row -->
  <AppHeader :user="user" :onLogin="login" :onLogout="logout" @toggle-sidebar="sidebarOpen = !sidebarOpen" />
  <div class="flex min-h-[calc(100vh-60px)] bg-[#2c2f33]">
    <!-- Sidebar (hidden when logged out) -->
    <template v-if="showSidebar">
      <!-- Desktop sidebar -->
      <div class="hidden md:block">
        <AppSidebar :router="router" />
      </div>

      <!-- Mobile overlay + drawer -->
      <div class="md:hidden">
        <div v-if="sidebarOpen" class="fixed inset-x-0 top-[60px] bottom-0 bg-black/60 z-40" @click="sidebarOpen = false"></div>
        <div
          class="fixed left-0 top-[60px] bottom-0 z-50 w-[260px] transform transition-transform duration-200 ease-out"
          :class="sidebarOpen ? 'translate-x-0' : '-translate-x-full'"
        >
          <AppSidebar mobile :router="router" @close="sidebarOpen = false" @navigate="sidebarOpen = false" />
        </div>
      </div>
    </template>
    <!-- Main content area -->
    <main class="flex-1 p-5">
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
