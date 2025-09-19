// Shared avatar utilities for consistent avatar handling across the app
export function useAvatar() {
  function getDiscordAvatar(user) {
    if (!user) return getDefaultAvatar();

    const { id, avatar, discriminator } = user;

    if (avatar) {
      const format = avatar.startsWith('a_') ? 'gif' : 'png';
      return `https://cdn.discordapp.com/avatars/${id}/${avatar}.${format}?size=256`;
    }

    // Default avatar based on discriminator or user ID
    const discriminator_num = discriminator || '0';
    const defaultNum = parseInt(discriminator_num) % 5;
    return `https://cdn.discordapp.com/embed/avatars/${defaultNum}.png`;
  }

  function getDefaultAvatar(username = '') {
    // Generate a default avatar URL based on username
    const hash = username ? username.length % 5 : 0;
    return `https://cdn.discordapp.com/embed/avatars/${hash}.png`;
  }

  function extractDiscordAvatarHash(avatarUrl) {
    if (!avatarUrl) return null;
    const match = avatarUrl.match(/avatars\/(\d+)\/([^/]+)\./);
    return match ? match[2] : null;
  }

  return {
    getDiscordAvatar,
    getDefaultAvatar,
    extractDiscordAvatarHash,
  };
}
