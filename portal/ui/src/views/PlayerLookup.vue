<template>
  <div class="max-w-5xl mx-auto mt-10 p-6 bg-gray-900 rounded-lg shadow-lg text-gray-100">
    <h2 class="text-2xl font-bold mb-4">Player Lookup</h2>
    <form v-if="!hasResults" @submit.prevent="lookupPlayer" class="mb-6">
      <div class="relative">
        <input
          v-model="userId"
          type="text"
          placeholder="Username, XPID, User ID, or Discord ID"
          class="w-full px-3 py-2 rounded bg-gray-800 text-white border border-gray-700 focus:outline-none focus:ring-2 focus:ring-purple-500 mb-4"
          required
          @input="onSearchInput"
          @focus="showAutocomplete = true"
          @blur="hideAutocompleteDelayed"
          @keydown.down.prevent="navigateAutocomplete(1)"
          @keydown.up.prevent="navigateAutocomplete(-1)"
          @keydown.enter.prevent="selectAutocompleteItem"
        />
        <!-- Autocomplete Dropdown -->
        <div
          v-if="showAutocomplete && autocompleteResults.length > 0"
          class="absolute z-10 w-full bg-gray-800 border border-gray-700 rounded-lg mt-[-12px] max-h-64 overflow-y-auto shadow-xl"
        >
          <button
            v-for="(result, index) in autocompleteResults"
            :key="result.user_id + result.display_name"
            type="button"
            :class="[
              'w-full px-3 py-2 text-left hover:bg-gray-700 flex items-center justify-between transition',
              index === selectedAutocompleteIndex ? 'bg-gray-700' : '',
            ]"
            @mousedown.prevent="selectResult(result)"
            @mouseenter="selectedAutocompleteIndex = index"
          >
            <div class="flex items-center gap-2">
              <span class="font-medium">{{ result.display_name }}</span>
              <span v-if="result.username && result.username !== result.display_name" class="text-xs text-gray-400">
                ({{ result.username }})
              </span>
            </div>
            <span class="text-xs text-gray-500 font-mono">{{ result.user_id.slice(0, 8) }}…</span>
          </button>
        </div>
      </div>
      <button
        type="submit"
        class="w-full px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white rounded transition font-semibold"
        :disabled="loading"
      >
        {{ loading ? 'Looking up...' : 'Lookup' }}
      </button>
    </form>

    <div v-if="error" class="text-red-400 mb-4">{{ error }}</div>

    <!-- Results -->
    <div v-if="hasResults" class="space-y-6">
      <!-- Account Card -->
      <div class="bg-gray-800 rounded-lg p-5">
        <div class="flex items-center justify-between">
          <div>
            <h3 class="text-xl font-semibold">EchoVRCE Account</h3>
            <p class="text-sm text-gray-400">
              Nakama ID:
              <span class="font-mono">{{ user?.id }}</span>
            </p>
          </div>
        </div>
        <div class="grid grid-cols-1 md:grid-cols-3 gap-4 mt-4">
          <div>
            <div class="text-sm text-gray-400">Username</div>
            <div class="text-base">{{ user?.username || '—' }}</div>
          </div>
          <div>
            <div class="text-sm text-gray-400">Discord ID</div>
            <div class="text-base font-mono">{{ user?.customId || '—' }}</div>
          </div>
          <div>
            <div class="text-sm text-gray-400">Created</div>
            <div class="text-base">{{ formatDate(user?.createTime) }}</div>
          </div>
          <div>
            <div class="text-sm text-gray-400">Last Seen</div>
            <div class="text-base">{{ lastSeenText }}</div>
          </div>
        </div>

        <div class="mt-4">
          <div class="text-sm text-gray-400 mb-1">Recent Logins</div>
          <div v-if="!canViewLoginHistory" class="text-gray-500 text-sm italic">
            Access restricted - Global Operator role required
          </div>
          <div
            v-else-if="recentLoginsText"
            class="whitespace-pre-wrap font-mono text-sm bg-gray-900 rounded p-3 border border-gray-700"
          >
            {{ recentLoginsText }}
          </div>
          <div v-else class="text-gray-400 text-sm">No login history.</div>
        </div>

        <div class="mt-4">
          <div class="text-sm text-gray-400 mb-1">Guild Memberships</div>
          <ul class="list-disc list-inside space-y-1">
            <li v-for="g in guildGroupList" :key="g.group.id" class="text-sm">
              {{ g.group.name }}
              <!-- guild roles -->
              <span v-if="userRolesByGuild.get(g.group.id)?.length" class="text-gray-400">
                -- [{{
                  userRolesByGuild
                    .get(g.group.id)
                    .map((r) => r.roleName)
                    .join(', ')
                }}]
              </span>
            </li>
          </ul>
          <div v-if="guildGroupList.length === 0" class="text-gray-400 text-sm">No groups.</div>
        </div>
      </div>

      <!-- Alternates Card -->
      <div v-if="canViewLoginHistory && alternateAccounts.length" class="bg-gray-800 rounded-lg p-5">
        <h3 class="text-xl font-semibold mb-2">Suspected Alternate Accounts</h3>
        <div class="space-y-4">
          <div
            v-for="alt in alternateAccounts"
            :key="alt.userId"
            class="border border-gray-700 rounded p-3 bg-gray-900"
          >
            <div class="text-sm mb-2">
              <button
                @click="lookupAlternateAccount(alt.username || alt.userId)"
                class="text-blue-400 hover:text-blue-300 underline font-medium"
              >
                {{ alt.username || alt.userId }}
              </button>
              <span v-if="alt.username" class="text-gray-500 font-mono text-xs ml-2">({{ alt.userId }})</span>
            </div>
            <ul class="list-disc list-inside text-sm space-y-1 mb-3">
              <li v-for="item in alt.items" :key="item">`{{ item }}`</li>
            </ul>

            <!-- Enforcement History for this alternate -->
            <div
              v-if="canViewEnforcement && alt.enforcement && getAltSuspensions(alt.enforcement).length"
              class="mt-3 pt-3 border-t border-gray-700"
            >
              <div class="text-xs font-semibold text-gray-400 mb-2">Enforcement History:</div>
              <div class="space-y-3">
                <div
                  v-for="grp in getAltSuspensions(alt.enforcement)"
                  :key="grp.groupId"
                  class="bg-gray-850 rounded px-3 py-2"
                >
                  <div class="text-xs font-medium text-gray-400 mb-2">{{ groupName(grp.groupId) }}</div>
                  <div class="space-y-2">
                    <div
                      v-for="rec in grp.records"
                      :key="rec.id"
                      :class="[
                        'border-l-2 pl-2 py-1 text-xs',
                        rec.isVoid ? 'border-gray-600 opacity-60' : getSuspensionBorderColor(rec),
                      ]"
                    >
                      <!-- Status -->
                      <div class="flex items-center gap-1 mb-1">
                        <span
                          :class="[
                            'px-1.5 py-0.5 text-[10px] font-semibold rounded',
                            rec.isVoid ? 'bg-gray-700 text-gray-400' : getSuspensionStatusClass(rec),
                          ]"
                        >
                          {{ rec.isVoid ? 'VOIDED' : getSuspensionStatus(rec) }}
                        </span>
                        <span v-if="rec.expiry && !rec.isVoid" class="text-xs font-medium text-gray-400">
                          {{ formatDuration(rec.expiry, rec.created_at) }}
                        </span>
                        <span v-else-if="!rec.isVoid" class="text-xs font-medium text-red-400">Permanent</span>
                      </div>

                      <!-- Suspension Notice -->
                      <div v-if="rec.suspension_notice" class="mb-1">
                        <div class="text-gray-300 text-xs">{{ rec.suspension_notice }}</div>
                      </div>

                      <!-- Date -->
                      <div class="text-[10px] text-gray-500">
                        {{ formatDate(rec.created_at) }} by {{ getEnforcerName(rec) }}
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Suspensions Card -->
      <div v-if="canViewEnforcement && suspensionsByGroup.length" class="bg-gray-800 rounded-lg p-5">
        <h3 class="text-xl font-semibold mb-4">Enforcement History</h3>
        <div class="space-y-4">
          <div
            v-for="grp in suspensionsByGroup"
            :key="grp.groupId"
            class="border border-gray-700 rounded-lg bg-gray-900"
          >
            <div class="bg-gray-850 px-4 py-2 border-b border-gray-700 rounded-t-lg">
              <div class="font-semibold text-lg">{{ groupName(grp.groupId) }}</div>
            </div>
            <div class="p-4 space-y-4">
              <div
                v-for="rec in grp.records"
                :key="rec.id"
                :class="[
                  'border-l-4 pl-4 py-2',
                  rec.isVoid ? 'border-gray-600 opacity-60' : getSuspensionBorderColor(rec),
                ]"
              >
                <!-- Status and Duration -->
                <div class="flex items-center gap-2 mb-2">
                  <span
                    :class="[
                      'px-2 py-0.5 text-xs font-semibold rounded',
                      rec.isVoid ? 'bg-gray-700 text-gray-400' : getSuspensionStatusClass(rec),
                    ]"
                  >
                    {{ rec.isVoid ? 'VOIDED' : getSuspensionStatus(rec) }}
                  </span>
                  <span v-if="rec.expiry && !rec.isVoid" class="text-sm font-medium text-gray-300">
                    {{ formatDuration(rec.expiry, rec.created_at) }} suspension
                  </span>
                  <span v-else-if="!rec.isVoid" class="text-sm font-medium text-red-400">Permanent</span>
                </div>

                <!-- Suspension Notice -->
                <div v-if="rec.suspension_notice" class="mb-2">
                  <div class="text-sm font-medium text-gray-400 mb-1">Reason:</div>
                  <div class="text-base text-gray-200 bg-gray-800 rounded px-3 py-2 border border-gray-700">
                    {{ rec.suspension_notice }}
                  </div>
                </div>

                <!-- Metadata -->
                <div class="text-sm text-gray-400 space-y-1">
                  <div class="flex items-center gap-2">
                    <span class="text-gray-500">Issued:</span>
                    <span>{{ formatDate(rec.created_at) }}</span>
                    <span class="text-gray-500">({{ formatRelative(rec.created_at) }})</span>
                  </div>
                  <div class="flex items-center gap-2">
                    <span class="text-gray-500">By:</span>
                    <span class="font-mono text-blue-400">
                      {{ getEnforcerName(rec) }}
                    </span>
                  </div>
                  <div v-if="rec.expiry" class="flex items-center gap-2">
                    <span class="text-gray-500">{{ isExpired(rec.expiry) ? 'Expired' : 'Expires' }}:</span>
                    <span>{{ formatDate(rec.expiry) }}</span>
                    <span :class="[isExpired(rec.expiry) ? 'text-gray-500' : 'text-yellow-400']">
                      ({{ expiryText(rec.expiry) }})
                    </span>
                  </div>
                </div>

                <!-- Moderator Notes -->
                <div v-if="rec.notes" class="mt-2 text-sm">
                  <span class="text-gray-500">Notes:</span>
                  <span class="text-gray-400 italic ml-2">{{ rec.notes }}</span>
                </div>

                <!-- Void Information -->
                <div v-if="rec.isVoid" class="mt-3 pt-3 border-t border-gray-700">
                  <div class="text-sm text-gray-400">
                    <div class="flex items-center gap-2 mb-1">
                      <span class="text-gray-500 font-medium">Voided by:</span>
                      <span class="font-mono text-gray-400">
                        {{ getVoidAuthorName(rec.void) }}
                      </span>
                      <span class="text-gray-600">•</span>
                      <span>{{ formatRelative(rec.void.voided_at) }}</span>
                    </div>
                    <div v-if="rec.void.notes" class="text-gray-500 italic ml-2">
                      {{ rec.void.notes }}
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Past Display Names Card -->
      <div v-if="pastDisplayNamesByGuild.length" class="bg-gray-800 rounded-lg p-5">
        <h3 class="text-xl font-semibold mb-4">Past Display Names</h3>
        <div class="space-y-4">
          <div
            v-for="group in pastDisplayNamesByGuild"
            :key="group.groupId"
            class="border border-gray-700 rounded-lg bg-gray-900"
          >
            <div class="bg-gray-850 px-4 py-2 border-b border-gray-700 rounded-t-lg">
              <div class="font-semibold">{{ groupName(group.groupId) }}</div>
            </div>
            <div class="p-3 space-y-2">
              <div
                v-for="entry in group.names"
                :key="entry.name"
                class="flex items-center justify-between gap-2 text-sm"
              >
                <div class="flex items-center gap-2">
                  <span class="font-mono text-gray-200">`{{ entry.name }}`</span>
                  <button
                    @click="copyToClipboard(entry.name)"
                    type="button"
                    class="px-2 py-0.5 text-xs rounded bg-gray-700 hover:bg-gray-600 border border-gray-600"
                  >
                    Copy
                  </button>
                  <span v-if="copiedName === entry.name" class="text-green-400 text-xs">Copied</span>
                </div>
                <span class="text-xs text-gray-500">{{ formatRelative(entry.timestamp) }}</span>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Game Servers Card -->
      <div v-if="gameServers.length" class="bg-gray-800 rounded-lg p-5">
        <h3 class="text-xl font-semibold mb-4">Active Game Servers</h3>
        <div class="space-y-3">
          <div
            v-for="server in gameServers"
            :key="server.match_id"
            class="border border-gray-700 rounded-lg bg-gray-900 p-4"
          >
            <!-- Server Header -->
            <div class="flex items-center justify-between mb-3">
              <div class="flex items-center gap-2">
                <span :class="['px-2 py-0.5 text-xs font-semibold rounded', getModeClass(server.label?.mode)]">
                  {{ formatMode(server.label?.mode) }}
                </span>
                <span
                  :class="[
                    'px-2 py-0.5 text-xs font-semibold rounded',
                    server.label?.open ? 'bg-green-900 text-green-200' : 'bg-red-900 text-red-200',
                  ]"
                >
                  {{ server.label?.open ? 'OPEN' : 'CLOSED' }}
                </span>
              </div>
              <span class="text-xs text-gray-500 font-mono">{{ server.match_id?.slice(0, 8) }}…</span>
            </div>

            <!-- Server Info Grid -->
            <div class="grid grid-cols-2 md:grid-cols-4 gap-3 text-sm">
              <div>
                <div class="text-xs text-gray-400">Players</div>
                <div class="font-medium">
                  {{ server.label?.player_count || 0 }} / {{ server.label?.player_limit || server.label?.limit || '?' }}
                </div>
              </div>
              <div>
                <div class="text-xs text-gray-400">Level</div>
                <div class="font-medium">{{ formatLevel(server.label?.level) }}</div>
              </div>
              <div>
                <div class="text-xs text-gray-400">Guild</div>
                <div class="font-medium">{{ groupName(server.label?.group_id) || '—' }}</div>
              </div>
              <div>
                <div class="text-xs text-gray-400">Location</div>
                <div class="font-medium">{{ getServerLocation(server.label) }}</div>
              </div>
            </div>

            <!-- Server Endpoint (Global Operators only) -->
            <div
              v-if="canViewServerIPs && server.label?.broadcaster?.endpoint"
              class="mt-3 pt-3 border-t border-gray-700"
            >
              <div class="text-xs text-gray-400 mb-1">Endpoint</div>
              <div class="font-mono text-xs text-gray-300 bg-gray-800 rounded px-2 py-1">
                {{ formatEndpoint(server.label?.broadcaster?.endpoint) }}
              </div>
            </div>

            <!-- Players List (if available) -->
            <div v-if="server.label?.players?.length" class="mt-3 pt-3 border-t border-gray-700">
              <div class="text-xs text-gray-400 mb-2">Players</div>
              <div class="flex flex-wrap gap-2">
                <span
                  v-for="player in server.label.players"
                  :key="player.user_id || player.display_name"
                  class="px-2 py-0.5 text-xs rounded bg-gray-800 border border-gray-700"
                >
                  {{ player.display_name || player.username || '?' }}
                </span>
              </div>
            </div>

            <!-- Match Timestamps -->
            <div class="mt-3 pt-3 border-t border-gray-700 flex items-center gap-4 text-xs text-gray-500">
              <span v-if="server.label?.created_at">Created: {{ formatRelative(server.label.created_at) }}</span>
              <span v-if="server.label?.start_time">Started: {{ formatRelative(server.label.start_time) }}</span>
            </div>
          </div>
        </div>
      </div>
      <div v-else-if="loadingServers" class="bg-gray-800 rounded-lg p-5">
        <h3 class="text-xl font-semibold mb-2">Active Game Servers</h3>
        <div class="text-gray-400">Loading game servers...</div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, ref, watch, onMounted, onUnmounted } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { get as apiGet, post as apiPost } from '../lib/apiClient.js';
import { userState } from '../composables/useUserState.js';

const route = useRoute();
const router = useRouter();

const userId = ref('');
const loading = ref(false);
const error = ref('');
const user = ref(null);
const loginHistory = ref(null);
const journal = ref(null);
const displayNameHistory = ref(null);
const guildGroups = ref(null);
const alternateUsernames = ref(new Map()); // Map of userId -> username
const enforcerUsernames = ref(new Map()); // Map of userId -> username for enforcers
const alternateEnforcement = ref(new Map()); // Map of userId -> enforcement journal
const autocompleteResults = ref([]);
const showAutocomplete = ref(false);
const selectedAutocompleteIndex = ref(-1);
const gameServers = ref([]); // Active game servers owned by this player
const loadingServers = ref(false);
let autocompleteTimeout = null;

const API_BASE = import.meta.env.VITE_NAKAMA_API_BASE;

// Permission checks
const isGlobalOperator = computed(() => {
  return userState.userGroups.includes('Global Operators');
});

const canViewLoginHistory = computed(() => isGlobalOperator.value);
const canViewEnforcement = computed(() => isGlobalOperator.value);
const canViewServerIPs = computed(() => isGlobalOperator.value);

// Store original title to restore on unmount
const originalTitle = document.title;

// Load identifier from route on mount
onMounted(() => {
  if (route.params.identifier && route.params.identifier !== ':identifier') {
    userId.value = decodeURIComponent(route.params.identifier);
    lookupPlayer();
  }
});

// Restore original title on unmount and cleanup timeouts
onUnmounted(() => {
  document.title = originalTitle;
  if (autocompleteTimeout) {
    clearTimeout(autocompleteTimeout);
    autocompleteTimeout = null;
  }
});

// Watch for route changes (when navigating between different player lookups)
watch(
  () => route.params.identifier,
  (newIdentifier) => {
    if (newIdentifier && newIdentifier !== ':identifier' && newIdentifier !== userId.value) {
      userId.value = decodeURIComponent(newIdentifier);
      lookupPlayer();
    } else if (!newIdentifier || newIdentifier === ':identifier') {
      // Clear the input when navigating to player lookup without an identifier
      userId.value = '';
      user.value = null;
      loginHistory.value = null;
      journal.value = null;
      displayNameHistory.value = null;
      guildGroups.value = null;
      gameServers.value = [];
      error.value = '';
      document.title = originalTitle;
    }
  }
);

// Autocomplete functions
async function searchDisplayNames(pattern) {
  if (!pattern || pattern.length < 2) {
    autocompleteResults.value = [];
    return;
  }

  try {
    const res = await apiGet(`/rpc/account/search?display_name=${encodeURIComponent(pattern.toLowerCase())}&limit=10`);
    if (res.ok) {
      const data = await res.json();
      autocompleteResults.value = (data.display_name_matches || []).map((item) => ({
        display_name: item.display_name,
        username: item.username,
        user_id: item.user_id,
      }));
    }
  } catch (e) {
    console.warn('Autocomplete search failed:', e);
    autocompleteResults.value = [];
  }
}

function onSearchInput() {
  // Clear existing timeout
  if (autocompleteTimeout) {
    clearTimeout(autocompleteTimeout);
  }

  // Reset selection
  selectedAutocompleteIndex.value = -1;

  // Debounce the search
  autocompleteTimeout = setTimeout(() => {
    searchDisplayNames(userId.value);
  }, 300);
}

function selectResult(result) {
  userId.value = result.display_name;
  showAutocomplete.value = false;
  autocompleteResults.value = [];
  selectedAutocompleteIndex.value = -1;
  lookupPlayer();
}

function hideAutocompleteDelayed() {
  // Delay hiding to allow click events to fire
  setTimeout(() => {
    showAutocomplete.value = false;
  }, 200);
}

function navigateAutocomplete(direction) {
  if (autocompleteResults.value.length === 0) return;

  const maxIndex = autocompleteResults.value.length - 1;
  selectedAutocompleteIndex.value += direction;

  if (selectedAutocompleteIndex.value > maxIndex) {
    selectedAutocompleteIndex.value = 0;
  } else if (selectedAutocompleteIndex.value < 0) {
    selectedAutocompleteIndex.value = maxIndex;
  }
}

function selectAutocompleteItem() {
  if (selectedAutocompleteIndex.value >= 0 && selectedAutocompleteIndex.value < autocompleteResults.value.length) {
    selectResult(autocompleteResults.value[selectedAutocompleteIndex.value]);
  } else {
    lookupPlayer();
  }
}

// Detect the type of identifier and return appropriate query parameter
// Supports:
// - UUIDs (8-4-4-4-12 format): user_id
// - Discord IDs (19-20 digit numbers): discord_id
// - XPIDs (formats like OVR-ORG-123, STM-123, etc.): xp_id
// - Default: username
function detectIdentifierType(identifier) {
  const trimmed = identifier.trim();

  // UUID pattern (user_id) - 8-4-4-4-12 hexadecimal format
  const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
  if (uuidPattern.test(trimmed)) {
    return { user_id: trimmed };
  }

  // Discord ID pattern (19-20 digits)
  const discordIdPattern = /^[0-9]{19,20}$/;
  if (discordIdPattern.test(trimmed)) {
    return { discord_id: trimmed };
  }

  // XPID pattern (e.g., OVR-ORG-123412341234, STM-12345, DMO-12345)
  // Matches alphanumeric segments separated by hyphens
  const xpidPattern = /^[A-Z0-9]+-[A-Z0-9-]+$/i;
  if (xpidPattern.test(trimmed) && trimmed.includes('-')) {
    return { xp_id: trimmed };
  }

  // Default to username for anything else
  return { username: trimmed };
}

async function lookupPlayer() {
  error.value = '';
  user.value = null;
  loginHistory.value = null;
  journal.value = null;
  displayNameHistory.value = null;
  gameServers.value = [];
  document.title = 'Player Lookup'; // Reset title during lookup
  loading.value = true;
  try {
    const input = userId.value.trim();
    if (!input) throw new Error('Please enter a username, XPID, User ID, or Discord ID.');

    // First, use the account/lookup RPC to resolve the identifier to a user ID
    const identifierParam = detectIdentifierType(input);
    const paramKey = Object.keys(identifierParam)[0];
    const paramValue = identifierParam[paramKey];

    const lookupRes = await apiGet(`/rpc/account/lookup?${paramKey}=${encodeURIComponent(paramValue)}`);
    if (!lookupRes.ok) {
      const errorData = await lookupRes.json().catch(() => ({}));
      throw new Error(errorData.message || 'User not found.');
    }

    const lookupData = await lookupRes.json();
    const uid = lookupData.id;

    if (!uid) throw new Error('User not found.');

    // Update URL with the user ID (canonical identifier)
    if (route.params.identifier !== uid) {
      router.push({ name: 'PlayerLookup', params: { identifier: encodeURIComponent(uid) } });
    }

    // Build storage object IDs based on permissions
    const storageObjectIds = [{ collection: 'DisplayName', key: 'history', userId: uid }];

    // Only fetch Login/history if user is a Global Operator
    if (canViewLoginHistory.value) {
      storageObjectIds.push({ collection: 'Login', key: 'history', userId: uid });
    }

    // Only fetch Enforcement/journal if user is a Global Operator
    if (canViewEnforcement.value) {
      storageObjectIds.push({ collection: 'Enforcement', key: 'journal', userId: uid });
    }

    // Now fetch user details, groups, and storage using the resolved user ID
    const [userRes, groupsRes, storageRes] = await Promise.all([
      apiGet(`/user?ids=${encodeURIComponent(uid)}`),
      apiGet(`/user/${encodeURIComponent(uid)}/group?limit=100`),
      apiPost(`/storage`, {
        objectIds: storageObjectIds,
      }),
    ]);

    if (!userRes.ok) throw new Error('User not found.');
    if (!groupsRes.ok) throw new Error('Failed to fetch user groups.');
    if (!storageRes.ok) throw new Error('Failed to fetch storage objects.');

    const usersJson = await userRes.json();
    const groupsJson = await groupsRes.json();
    const storageJson = await storageRes.json();

    const u = (usersJson && usersJson.users && usersJson.users[0]) || null;
    if (!u) throw new Error('User not found.');
    user.value = u;

    // Update page title with username
    document.title = `${u.username || u.displayName || uid} - Player Lookup`;

    const memberGroupIds = (groupsJson['user_groups'] || []).filter((g) => g.state <= 2).map((g) => g.group.id);
    if (memberGroupIds.length === 0) {
      guildGroups.value = [];
    } else {
      try {
        const res = await apiGet(`/rpc/guildgroup?ids=${memberGroupIds.map(encodeURIComponent).join(',')}`);
        if (!res.ok) throw new Error('Failed to fetch guild groups.');
        const guildGroupsJson = await res.json();
        guildGroups.value = guildGroupsJson.guild_groups || [];
      } catch (e) {
        error.value = e?.message || 'Failed to fetch guild groups.';
        guildGroups.value = [];
      }
    }

    // Map storage objects by key
    const objects = (storageJson && storageJson.objects) || [];
    const byKey = new Map();
    for (const obj of objects) {
      byKey.set(`${obj.collection}/${obj.key}`, obj);
    }
    loginHistory.value = parseStorageValue(byKey.get('Login/history'));
    journal.value = parseStorageValue(byKey.get('Enforcement/journal'));
    displayNameHistory.value = parseStorageValue(byKey.get('DisplayName/history'));

    // Resolve alternate account usernames
    await resolveAlternateUsernames();

    // Resolve enforcer usernames
    await resolveEnforcerUsernames();

    // Fetch enforcement data for alternate accounts
    await fetchAlternateEnforcement();

    // Fetch game servers owned by this player
    await fetchGameServers(uid);
  } catch (e) {
    error.value = e?.message || 'Lookup failed.';
  } finally {
    loading.value = false;
  }
}

// Resolve usernames for alternate accounts
async function resolveAlternateUsernames() {
  const hist = loginHistory.value;
  if (!hist || !hist.alternate_accounts) return;

  const userIds = Object.keys(hist.alternate_accounts);
  if (userIds.length === 0) return;

  const usernameMap = new Map();

  // Fetch usernames in parallel
  await Promise.all(
    userIds.map(async (userId) => {
      try {
        const res = await apiGet(`/rpc/account/lookup?user_id=${encodeURIComponent(userId)}`);
        if (res.ok) {
          const data = await res.json();
          if (data.username) {
            usernameMap.set(userId, data.username);
          }
        }
      } catch (e) {
        // Silently fail for individual lookups
        console.warn(`Failed to resolve username for ${userId}:`, e);
      }
    })
  );

  alternateUsernames.value = usernameMap;
}

// Resolve usernames for enforcers in suspension records
async function resolveEnforcerUsernames() {
  const j = journal.value;
  if (!j || !j.records) return;

  const enforcerIds = new Set();

  // Collect all unique enforcer user IDs
  for (const records of Object.values(j.records)) {
    for (const rec of records || []) {
      if (rec.enforcer_user_id) {
        enforcerIds.add(rec.enforcer_user_id);
      }
    }
  }

  // Also collect void author IDs
  const voidsByGroup = j.voids || j.voidsByRecordIDByGroupID || j.voids_by_record_id_by_group_id || {};
  for (const groupVoids of Object.values(voidsByGroup)) {
    for (const voidRecord of Object.values(groupVoids || {})) {
      const userId = voidRecord.user_id || voidRecord.author_id;
      if (userId) {
        enforcerIds.add(userId);
      }
    }
  }

  if (enforcerIds.size === 0) return;

  const usernameMap = new Map();

  // Fetch usernames in parallel
  await Promise.all(
    Array.from(enforcerIds).map(async (userId) => {
      try {
        const res = await apiGet(`/rpc/account/lookup?user_id=${encodeURIComponent(userId)}`);
        if (res.ok) {
          const data = await res.json();
          if (data.username) {
            usernameMap.set(userId, data.username);
          }
        }
      } catch (e) {
        // Silently fail for individual lookups
        console.warn(`Failed to resolve username for enforcer ${userId}:`, e);
      }
    })
  );

  enforcerUsernames.value = usernameMap;
}

// Fetch enforcement journal for alternate accounts
async function fetchAlternateEnforcement() {
  const hist = loginHistory.value;
  if (!hist || !hist.alternate_accounts) return;

  const userIds = Object.keys(hist.alternate_accounts);
  if (userIds.length === 0) return;

  const enforcementMap = new Map();

  // Fetch enforcement data for each alternate account
  await Promise.all(
    userIds.map(async (altUserId) => {
      try {
        if (!canViewEnforcement.value) return;

        const res = await apiPost(`/storage`, {
          objectIds: [{ collection: 'Enforcement', key: 'journal', userId: altUserId }],
        });

        if (res.ok) {
          const data = await res.json();
          const objects = (data && data.objects) || [];
          if (objects.length > 0) {
            const obj = objects[0];
            if (obj.value) {
              try {
                const journal = JSON.parse(obj.value);
                enforcementMap.set(altUserId, journal);
              } catch (e) {
                console.warn(`Failed to parse enforcement for ${altUserId}:`, e);
              }
            }
          }
        }
      } catch (e) {
        console.warn(`Failed to fetch enforcement for alternate ${altUserId}:`, e);
      }
    })
  );

  alternateEnforcement.value = enforcementMap;
}

// Fetch active game servers owned by the user
async function fetchGameServers(targetUserId) {
  loadingServers.value = true;
  gameServers.value = [];

  try {
    // Fetch all matches - the backend AfterListMatches hook will filter based on permissions
    const res = await apiGet('/match?limit=100&authoritative=true');
    if (!res.ok) {
      console.warn('Failed to fetch matches');
      return;
    }

    const data = await res.json();
    const matches = data.matches || [];

    // Filter to only matches where this user is the operator
    const userServers = [];
    for (const match of matches) {
      try {
        const label = match.label?.value ? JSON.parse(match.label.value) : match.label;
        if (!label) continue;

        // Check if this user owns the server
        const operatorId = label.broadcaster?.oper || label.broadcaster?.operator_id;
        if (operatorId === targetUserId) {
          userServers.push({
            match_id: match.match_id,
            label,
          });
        }
      } catch (e) {
        console.warn('Failed to parse match label:', e);
      }
    }

    gameServers.value = userServers;
  } catch (e) {
    console.warn('Failed to fetch game servers:', e);
  } finally {
    loadingServers.value = false;
  }
}

function parseStorageValue(obj) {
  if (!obj || !obj.value) return null;
  try {
    return JSON.parse(obj.value);
  } catch (_) {
    return null;
  }
}

// auth headers and auto-refresh handled by apiClient

const hasResults = computed(() => !!user.value);

const guildGroupList = computed(() => {
  if (!guildGroups.value) return [];
  return guildGroups.value;
});

// A map of guild ID -> roles for the user based on
// the guild group's role_cache
const userRolesByGuild = computed(() => {
  const map = new Map();
  if (!guildGroups.value || !user.value) return map;

  for (const g of guildGroupList.value) {
    const userRoles = [];
    for (const [roleName, roleId] of Object.entries(g.roles || {})) {
      if (g.state.role_cache && g.state.role_cache[roleId] && g.state.role_cache[roleId][user.value.id]) {
        userRoles.push({ roleName, roleId });
      }
    }
    // Sort the roles
    userRoles.sort((a, b) => a.roleName.localeCompare(b.roleName));
    map.set(g.group.id, userRoles);
  }
  return map;
});

function formatDate(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleString();
}

function formatRelative(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  const now = new Date();
  const diffMs = now - d;
  const seconds = Math.floor(diffMs / 1000);
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days}d ago`;
  const months = Math.floor(days / 30);
  if (months < 12) return `${months}mo ago`;
  const years = Math.floor(months / 12);
  return `${years}y ago`;
}

const lastSeenText = computed(() => {
  // Prefer online flag if present
  if (user.value?.online) return 'online now';
  const latest = latestLoginTimestamp();
  return latest ? formatRelative(latest) : '—';
});

function latestLoginTimestamp() {
  const hist = loginHistory.value;
  if (!hist || !hist.history) return null;
  let latest = null;
  for (const key of Object.keys(hist.history)) {
    const e = hist.history[key];
    const ts = e.update_time || e.create_time;
    if (!ts) continue;
    if (!latest || new Date(ts) > new Date(latest)) latest = ts;
  }
  return latest;
}

const recentLoginsText = computed(() => {
  const hist = loginHistory.value;
  if (!hist || !hist.history) return '';
  // Map by XPID token -> latest time
  const latestByXpid = new Map();
  for (const key of Object.keys(hist.history)) {
    const e = hist.history[key];
    const xpid = e?.xpi?.token || e?.xpi || 'unknown';
    const ts = e.update_time || e.create_time;
    if (!ts) continue;
    const prev = latestByXpid.get(xpid);
    if (!prev || new Date(ts) > new Date(prev)) latestByXpid.set(xpid, ts);
  }
  const lines = Array.from(latestByXpid.entries())
    .map(([xpid, ts]) => `${formatRelative(ts)} - \`${xpid}\``)
    .sort()
    .reverse();
  return lines.join('\n');
});

const groupNameMap = computed(() => {
  const map = new Map();
  for (const g of guildGroupList.value) {
    map.set(g.group.id, g.group.name);
  }
  return map;
});

function groupName(id) {
  return groupNameMap.value.get(id) || id;
}

// Get enforcer display name (username or fallback to ID)
function getEnforcerName(rec) {
  if (rec.enforcer_discord_id) {
    return `@${rec.enforcer_discord_id}`;
  }
  const username = enforcerUsernames.value.get(rec.enforcer_user_id);
  return username || rec.enforcer_user_id;
}

// Get void author display name
function getVoidAuthorName(voidRec) {
  if (voidRec.discord_id) {
    return `@${voidRec.discord_id}`;
  }
  const username = enforcerUsernames.value.get(voidRec.user_id);
  return username || voidRec.user_id;
}

// Suspensions: gather all records per group
const suspensionsByGroup = computed(() => {
  const j = journal.value;
  if (!j || !j.records) return [];
  const voidsByGroup = j.voids || j.voidsByRecordIDByGroupID || j.voids_by_record_id_by_group_id || {};
  const out = [];
  for (const [groupId, records] of Object.entries(j.records)) {
    const groupVoids = voidsByGroup[groupId] || {};
    // Normalize date keys to consistent snake_case for rendering safety
    const recs = (records || []).map((r) => {
      const rid = r.id;
      const v = groupVoids[rid];
      return {
        id: rid,
        user_id: r.user_id,
        group_id: r.group_id || groupId,
        enforcer_user_id: r.enforcer_user_id,
        enforcer_discord_id: r.enforcer_discord_id,
        created_at: r.created_at || r.createdAt,
        updated_at: r.updated_at || r.updatedAt,
        suspension_notice: r.suspension_notice,
        expiry: r.suspension_expiry || r.expiry,
        notes: r.notes,
        isVoid: !!(v && (v.voided_at || v.voidedAt)),
        void: v
          ? {
              group_id: v.group_id || groupId,
              record_id: v.record_id || rid,
              user_id: v.user_id || v.author_id,
              discord_id: v.discord_id || v.author_discord_id,
              voided_at: v.voided_at || v.voidedAt,
              notes: v.notes,
            }
          : null,
      };
    });
    out.push({ groupId, records: recs });
  }
  // Sort groups by name for stable UI
  out.sort((a, b) => groupName(a.groupId).localeCompare(groupName(b.groupId)));
  return out;
});

// Helper function to process enforcement journal for alternate accounts
function getAltSuspensions(journal) {
  if (!journal || !journal.records) return [];
  const voidsByGroup =
    journal.voids || journal.voidsByRecordIDByGroupID || journal.voids_by_record_id_by_group_id || {};
  const out = [];
  for (const [groupId, records] of Object.entries(journal.records)) {
    const groupVoids = voidsByGroup[groupId] || {};
    const recs = (records || []).map((r) => {
      const rid = r.id;
      const v = groupVoids[rid];
      return {
        id: rid,
        user_id: r.user_id,
        group_id: r.group_id || groupId,
        enforcer_user_id: r.enforcer_user_id,
        enforcer_discord_id: r.enforcer_discord_id,
        created_at: r.created_at || r.createdAt,
        updated_at: r.updated_at || r.updatedAt,
        suspension_notice: r.suspension_notice,
        expiry: r.suspension_expiry || r.expiry,
        notes: r.notes,
        isVoid: !!(v && (v.voided_at || v.voidedAt)),
        void: v
          ? {
              group_id: v.group_id || groupId,
              record_id: v.record_id || rid,
              user_id: v.user_id || v.author_id,
              discord_id: v.discord_id || v.author_discord_id,
              voided_at: v.voided_at || v.voidedAt,
              notes: v.notes,
            }
          : null,
      };
    });
    out.push({ groupId, records: recs });
  }
  // Sort groups by name for stable UI
  out.sort((a, b) => groupName(a.groupId).localeCompare(groupName(b.groupId)));
  return out;
}

function isExpired(iso) {
  if (!iso) return false;
  return new Date(iso) <= new Date();
}

function expiryText(iso) {
  if (!iso) return '';
  const exp = new Date(iso);
  const now = new Date();
  return exp > now ? `expires ${formatRelative(iso)}` : `expired ${formatRelative(iso)}`;
}

function getSuspensionStatus(rec) {
  if (!rec.expiry) return 'PERMANENT';
  return isExpired(rec.expiry) ? 'EXPIRED' : 'ACTIVE';
}

function getSuspensionStatusClass(rec) {
  if (!rec.expiry) return 'bg-red-900 text-red-200';
  return isExpired(rec.expiry) ? 'bg-gray-700 text-gray-300' : 'bg-orange-900 text-orange-200';
}

function getSuspensionBorderColor(rec) {
  if (!rec.expiry) return 'border-red-500';
  return isExpired(rec.expiry) ? 'border-gray-500' : 'border-orange-500';
}

function formatDuration(expiryIso, createdIso) {
  if (!expiryIso || !createdIso) return '';
  const start = new Date(createdIso);
  const end = new Date(expiryIso);
  let diff = Math.max(0, end - start);
  const seconds = Math.floor(diff / 1000);
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);
  if (days > 0) return `${days}d`;
  if (hours > 0) return `${hours}h`;
  if (minutes > 0) return `${minutes}m`;
  return `${seconds}s`;
}

// Alternates
const alternateAccounts = computed(() => {
  const hist = loginHistory.value;
  if (!hist || !hist.alternate_accounts) return [];
  const out = [];
  for (const [altUserId, matches] of Object.entries(hist.alternate_accounts)) {
    const items = new Set();
    (matches || []).forEach((m) => {
      // Try common shapes: items, patterns, or raw fields
      if (Array.isArray(m.items)) m.items.forEach((x) => items.add(x));
      if (Array.isArray(m.patterns)) m.patterns.forEach((x) => items.add(x));
      // Include any simple string fields if present
      ['client_ip', 'xpi', 'hmd_serial', 'system_profile'].forEach((k) => {
        if (m[k]) items.add(m[k]);
      });
    });
    out.push({
      userId: altUserId,
      username: alternateUsernames.value.get(altUserId) || null,
      items: Array.from(items),
      enforcement: alternateEnforcement.value.get(altUserId) || null,
    });
  }
  return out;
});

// Look up an alternate account
function lookupAlternateAccount(identifier) {
  router.push({ name: 'PlayerLookup', params: { identifier: encodeURIComponent(identifier) } });
}

// Past display names grouped by guild
const pastDisplayNamesByGuild = computed(() => {
  const h = displayNameHistory.value;
  if (!h) return [];
  const histories = h.history || {};
  const result = [];

  for (const [groupId, nameMap] of Object.entries(histories)) {
    if (!nameMap || Object.keys(nameMap).length === 0) continue;

    // Sort names by timestamp, most recent first
    const names = Object.entries(nameMap)
      .map(([name, ts]) => ({ name, timestamp: ts }))
      .sort((a, b) => new Date(b.timestamp) - new Date(a.timestamp))
      .slice(0, 10); // Limit to 10 per guild

    if (names.length > 0) {
      result.push({
        groupId,
        names,
      });
    }
  }

  // Sort guilds by the most recent name usage
  result.sort((a, b) => {
    const aLatest = a.names[0]?.timestamp || '';
    const bLatest = b.names[0]?.timestamp || '';
    return new Date(bLatest) - new Date(aLatest);
  });

  return result;
});

// Clipboard helpers
const copiedName = ref('');
async function copyToClipboard(text) {
  try {
    await navigator.clipboard.writeText(text);
    copiedName.value = text;
    setTimeout(() => {
      if (copiedName.value === text) copiedName.value = '';
    }, 1200);
  } catch (e) {
    // no-op; could surface an error toast
  }
}

// Game server helper functions
function formatMode(mode) {
  if (!mode) return 'Unknown';
  // Handle symbol string or raw mode string
  const modeStr = String(mode).toLowerCase();
  if (modeStr.includes('arena')) return 'Arena';
  if (modeStr.includes('combat')) return 'Combat';
  if (modeStr.includes('social')) return 'Social';
  return mode;
}

function getModeClass(mode) {
  if (!mode) return 'bg-gray-700 text-gray-300';
  const modeStr = String(mode).toLowerCase();
  if (modeStr.includes('arena')) return 'bg-blue-900 text-blue-200';
  if (modeStr.includes('combat')) return 'bg-red-900 text-red-200';
  if (modeStr.includes('social')) return 'bg-green-900 text-green-200';
  return 'bg-gray-700 text-gray-300';
}

function formatLevel(level) {
  if (!level) return '—';
  // Handle symbol string or raw level string
  const levelStr = String(level);
  // Extract the level name from the symbol (e.g., "mpl_arena_a" -> "Arena A")
  return levelStr
    .replace(/^mpl_/, '')
    .replace(/_/g, ' ')
    .replace(/\b\w/g, (c) => c.toUpperCase());
}

function getServerLocation(label) {
  if (!label) return '—';
  const parts = [];
  if (label.server_city || label.broadcaster?.city) {
    parts.push(label.server_city || label.broadcaster?.city);
  }
  if (label.server_country || label.broadcaster?.country_code) {
    parts.push(label.server_country || label.broadcaster?.country_code);
  }
  if (label.server_region || label.broadcaster?.default_region) {
    parts.push(label.server_region || label.broadcaster?.default_region);
  }
  return parts.length > 0 ? parts.join(', ') : '—';
}

function formatEndpoint(endpoint) {
  if (!endpoint) return '—';
  // Endpoint may have internal_ip, external_ip, port
  const parts = [];
  if (endpoint.external_ip) {
    parts.push(`External: ${endpoint.external_ip}:${endpoint.port || '?'}`);
  }
  if (endpoint.internal_ip && endpoint.internal_ip !== endpoint.external_ip) {
    parts.push(`Internal: ${endpoint.internal_ip}:${endpoint.port || '?'}`);
  }
  return parts.length > 0 ? parts.join(' | ') : JSON.stringify(endpoint);
}
</script>

<style scoped></style>
