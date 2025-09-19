<template>
  <div class="flex flex-col items-center justify-center min-h-screen bg-gray-900 text-center p-8">
    <!-- Loading State -->
    <div v-if="loading" class="flex flex-col items-center">
      <div class="animate-spin rounded-full h-16 w-16 border-b-2 border-purple-500 mb-6"></div>
      <h2 class="text-2xl font-semibold text-white mb-3">{{ status }}</h2>
      <p class="text-gray-300">Please wait while we complete your login...</p>
    </div>

    <!-- Success State -->
    <div v-else-if="success && userProfile" class="flex flex-col items-center">
      <div class="mb-6">
        <img
          :src="userAvatar"
          :alt="`${userProfile.username}'s avatar`"
          class="w-24 h-24 rounded-full border-4 border-green-500 shadow-lg"
        />
      </div>
      <div class="flex items-center mb-4">
        <svg class="w-8 h-8 text-green-500 mr-3" fill="currentColor" viewBox="0 0 20 20">
          <path
            fill-rule="evenodd"
            d="M10 18a8 8 0 100-16 8 8 0 000 16zm3.707-9.293a1 1 0 00-1.414-1.414L9 10.586 7.707 9.293a1 1 0 00-1.414 1.414l2 2a1 1 0 001.414 0l4-4z"
          />
        </svg>
        <h2 class="text-2xl font-semibold text-green-500">Welcome back, {{ userProfile.username }}!</h2>
      </div>
      <p class="text-gray-300 mb-6">You have been successfully logged in with Discord.</p>
      <div class="flex space-x-4">
        <button
          @click="goToApplications"
          class="px-6 py-3 bg-purple-600 hover:bg-purple-700 text-white rounded-lg transition font-medium"
        >
          Go to Applications
        </button>
        <button
          @click="goHome"
          class="px-6 py-3 bg-gray-600 hover:bg-gray-700 text-white rounded-lg transition font-medium"
        >
          Go to Dashboard
        </button>
      </div>
    </div>

    <!-- Error State -->
    <div v-else-if="error" class="flex flex-col items-center">
      <div class="mb-6">
        <svg class="w-16 h-16 text-red-500" fill="currentColor" viewBox="0 0 20 20">
          <path
            fill-rule="evenodd"
            d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7 4a1 1 0 11-2 0 1 1 0 012 0zm-1-9a1 1 0 00-1 1v4a1 1 0 102 0V6a1 1 0 00-1-1z"
          />
        </svg>
      </div>
      <h2 class="text-2xl font-semibold text-red-500 mb-3">Authentication Failed</h2>
      <p class="text-gray-300 mb-6">{{ error }}</p>
      <button
        @click="goHome"
        class="px-6 py-3 bg-blue-600 hover:bg-blue-700 text-white rounded-lg transition font-medium"
      >
        Try Again
      </button>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted, computed } from 'vue';
import { useRouter } from 'vue-router';
import { useAvatar } from '../../composables/useAvatar.js';
import { userState } from '../../composables/useUserState.js';

const router = useRouter();

const API_BASE = import.meta.env.VITE_NAKAMA_API_BASE;
const NAKAMA_SERVER_KEY = import.meta.env.VITE_NAKAMA_SERVER_KEY;

const loading = ref(true);
const success = ref(false);
const error = ref('');
const status = ref('Connecting to Discord...');
// Use the global userState for profile

const { getDiscordAvatar, extractDiscordAvatarHash } = useAvatar();

const userAvatar = computed(() => {
  return getDiscordAvatar(userState.profile);
});

async function authenticateWithDiscord(code) {
  try {
    status.value = 'Exchanging authorization code...';
    // Exchange code for tokens with Nakama
    const response = await fetch(`${API_BASE}/account/authenticate/custom?unwrap`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Basic ${btoa(`${NAKAMA_SERVER_KEY}:`)}`,
      },
      body: JSON.stringify({ id: 'discord:' + code }),
    });

    const data = await response.json();

    if (!response.ok) {
      throw new Error(data.message || 'Authentication failed');
    }

    if (data.token && data.refresh_token) {
      status.value = 'Fetching user profile...';

      // Store tokens
      localStorage.setItem('jwt', data.token);
      localStorage.setItem('refreshToken', data.refresh_token);

      // Get user profile from Nakama
      const userResponse = await fetch(`${API_BASE}/account`, {
        headers: {
          Authorization: `Bearer ${data.token}`,
        },
      });

      if (userResponse.ok) {
        const userData = await userResponse.json();
        userState.profile = {
          id: userData.custom_id || userData.user.id,
          username: userData.user.username,
          avatar: userData.user.avatar_url ? extractDiscordAvatarHash(userData.user.avatar_url) : null,
          discriminator: userData.user.display_name,
        };
      }

      // Set global tokens
      userState.token = data.token;
      userState.refreshToken = data.refresh_token;

      // Dispatch custom event to notify App.vue of successful login
      window.dispatchEvent(
        new CustomEvent('userLoggedIn', {
          detail: { token: data.token, refreshToken: data.refresh_token, user: userState.profile },
        })
      );

      success.value = true;
      loading.value = false;
      status.value = 'Login successful!';

      // Clean up URL and handle redirect
      window.history.replaceState({}, document.title, window.location.pathname);

      // Check for state parameter to redirect back to original path
      const stateParam = new URL(window.location.href).searchParams.get('state');
      if (stateParam) {
        try {
          const originalPath = atob(stateParam); // decode base64
          setTimeout(() => router.push(originalPath), 2000);
          return;
        } catch (e) {
          console.warn('Invalid state parameter:', e);
        }
      }
    } else {
      throw new Error('Invalid response from server');
    }
  } catch (err) {
    console.error('Discord authentication error:', err);
    error.value = err.message || 'An unexpected error occurred during authentication';
    loading.value = false;
  }
}

function goToApplications() {
  router.push('/applications');
}

function goHome() {
  // Check for state parameter first
  const stateParam = new URL(window.location.href).searchParams.get('state');
  if (stateParam) {
    try {
      const originalPath = atob(stateParam);
      router.push(originalPath);
      return;
    } catch (e) {
      console.warn('Invalid state parameter:', e);
    }
  }
  router.push('/');
}

// Utility to extract Discord OAuth code from both query string and hash fragment
function getDiscordAuthCode() {
  // Try to get the code from the query string (?code=...)
  const url = new URL(window.location.href);
  let code = url.searchParams.get('code');
  if (code) return code;

  // If not found, try to get it from the hash fragment (#/auth/discord/callback?code=...)
  const hash = window.location.hash;
  if (hash && hash.includes('code=')) {
    const hashParams = new URLSearchParams(hash.split('?')[1]);
    code = hashParams.get('code');
    if (code) return code;
  }
  return null;
}

onMounted(async () => {
  // Check for OAuth error from Discord
  const errorParam = new URL(window.location.href).searchParams.get('error');
  if (errorParam) {
    error.value = `Discord authorization failed: ${errorParam}`;
    loading.value = false;
    return;
  }

  // Extract code from both query and hash
  const code = getDiscordAuthCode();
  if (!code) {
    error.value = 'No authorization code received from Discord';
    loading.value = false;
    return;
  }

  await authenticateWithDiscord(code);
});
</script>
