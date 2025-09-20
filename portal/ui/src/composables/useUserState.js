import { reactive } from 'vue';

// This is a global reactive user state object
export const userState = reactive({
  profile: null, // { id, username, avatar, discriminator }
  token: null,
  refreshToken: null,
});
