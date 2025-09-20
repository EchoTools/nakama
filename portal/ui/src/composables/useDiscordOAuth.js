import { computed } from 'vue';

export function useDiscordOAuth() {
  const DISCORD_CLIENT_ID = import.meta.env.VITE_DISCORD_CLIENT_ID;
  const DISCORD_REDIRECT_URI = import.meta.env.VITE_DISCORD_REDIRECT_URI;

  // Accepts a path string, returns the full Discord OAuth URL with state param
  function getDiscordOAuthUrl(currentPath = '/') {
    if (!DISCORD_CLIENT_ID || !DISCORD_REDIRECT_URI) return null;
    const state = btoa(currentPath);
    return (
      `https://discord.com/api/oauth2/authorize?client_id=${DISCORD_CLIENT_ID}` +
      `&redirect_uri=${encodeURIComponent(DISCORD_REDIRECT_URI)}` +
      `&response_type=code&scope=identify&state=${encodeURIComponent(state)}`
    );
  }

  // Navigates to the Discord OAuth URL, passing the current path as state
  function loginDiscord(currentPath = window.location.pathname) {
    const url = getDiscordOAuthUrl(currentPath);
    if (!url) return;
    window.location = url;
  }

  return {
    getDiscordOAuthUrl,
    loginDiscord,
  };
}
