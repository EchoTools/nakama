<template>
  <div class="max-w-5xl mx-auto mt-10 p-6 bg-gray-900 rounded-lg shadow-lg text-gray-100">
    <h2 class="text-2xl font-bold mb-4">Player Lookup</h2>
    <form @submit.prevent="lookupPlayer" class="mb-6">
      <label class="block text-gray-300 mb-2">User ID</label>
      <input
        v-model="userId"
        type="text"
        placeholder="Enter User ID"
        class="w-full px-3 py-2 rounded bg-gray-800 text-white border border-gray-700 focus:outline-none focus:ring-2 focus:ring-purple-500 mb-4"
        required
      />
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
          <div
            class="whitespace-pre-wrap font-mono text-sm bg-gray-900 rounded p-3 border border-gray-700"
            v-if="recentLoginsText"
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
      <div v-if="alternateAccounts.length" class="bg-gray-800 rounded-lg p-5">
        <h3 class="text-xl font-semibold mb-2">Suspected Alternate Accounts</h3>
        <div class="space-y-4">
          <div
            v-for="alt in alternateAccounts"
            :key="alt.userId"
            class="border border-gray-700 rounded p-3 bg-gray-900"
          >
            <div class="font-mono text-sm mb-2">{{ alt.userId }}</div>
            <ul class="list-disc list-inside text-sm space-y-1">
              <li v-for="item in alt.items" :key="item">`{{ item }}`</li>
            </ul>
          </div>
        </div>
      </div>

      <!-- Suspensions Card -->
      <div v-if="suspensionsByGroup.length" class="bg-gray-800 rounded-lg p-5">
        <h3 class="text-xl font-semibold mb-2">Suspensions</h3>
        <div class="space-y-4">
          <div
            v-for="grp in suspensionsByGroup"
            :key="grp.groupId"
            class="border border-gray-700 rounded p-3 bg-gray-900"
          >
            <div class="font-semibold mb-2">{{ groupName(grp.groupId) }}</div>
            <ul class="space-y-3">
              <li v-for="rec in grp.records" :key="rec.id" class="text-sm">
                <div :class="['text-gray-300', rec.isVoid ? 'line-through' : '']">
                  {{ formatRelative(rec.created_at) }} by
                  <span class="font-mono">
                    {{ rec.enforcer_discord_id ? '@' + rec.enforcer_discord_id : rec.enforcer_user_id }}
                  </span>
                  <span v-if="rec.expiry">
                    for
                    <strong>{{ formatDuration(rec.expiry, rec.created_at) }}</strong>
                    ({{ expiryText(rec.expiry) }})
                  </span>
                </div>
                <div v-if="rec.suspension_notice" class="font-mono">- `{{ rec.suspension_notice }}`</div>
                <div v-if="rec.notes" class="suspensionitalic text-gray-400">- *{{ rec.notes }}*</div>
                <div v-if="rec.isVoid" class="text-gray-400">
                  - voided by
                  <span class="font-mono">
                    {{ rec.void.discord_id ? '@' + rec.void.discord_id : rec.void.user_id }}
                  </span>
                  {{ formatRelative(rec.void.voided_at) }}
                  <span v-if="rec.void.notes" class="italic">*{{ rec.void.notes }}*</span>
                </div>
              </li>
            </ul>
          </div>
        </div>
      </div>

      <!-- Past Display Names Card -->
      <div v-if="pastDisplayNames.length" class="bg-gray-800 rounded-lg p-5">
        <h3 class="text-xl font-semibold mb-2">Past Display Names</h3>
        <div class="text-sm space-y-1">
          <div v-for="name in pastDisplayNames" :key="name" class="flex items-center gap-2">
            <span class="font-mono">`{{ name }}`</span>
            <button
              @click="copyToClipboard(name)"
              type="button"
              class="px-2 py-0.5 text-xs rounded bg-gray-700 hover:bg-gray-600 border border-gray-600"
            >
              Copy
            </button>
            <span v-if="copiedName === name" class="text-green-400 text-xs">Copied</span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, ref, watch } from 'vue';
import { get as apiGet, post as apiPost, callRpc } from '../lib/apiClient.js';

const userId = ref('');
const loading = ref(false);
const error = ref('');
const user = ref(null);
const loginHistory = ref(null);
const journal = ref(null);
const displayNameHistory = ref(null);
const guildGroups = ref(null);

const API_BASE = import.meta.env.VITE_NAKAMA_API_BASE;

async function lookupPlayer() {
  error.value = '';
  user.value = null;
  loginHistory.value = null;
  journal.value = null;
  displayNameHistory.value = null;
  loading.value = true;
  try {
    const uid = userId.value.trim();
    if (!uid) throw new Error('Please enter a user ID.');

    const [userRes, groupsRes, storageRes] = await Promise.all([
      apiGet(`/user?ids=${encodeURIComponent(uid)}`),
      apiGet(`/user/${encodeURIComponent(uid)}/group?limit=100`),
      apiPost(`/storage`, {
        objectIds: [
          { collection: 'Login', key: 'history', userId: uid },
          { collection: 'Enforcement', key: 'journal', userId: uid },
          { collection: 'DisplayName', key: 'history', userId: uid },
        ],
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
  } catch (e) {
    error.value = e?.message || 'Lookup failed.';
  } finally {
    loading.value = false;
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

function expiryText(iso) {
  if (!iso) return '';
  const exp = new Date(iso);
  const now = new Date();
  return exp > now ? `expires ${formatRelative(iso)}` : `expired ${formatRelative(iso)}`;
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
    out.push({ userId: altUserId, items: Array.from(items) });
  }
  return out;
});

// Past display names (top ~10 unique, most recent)
const pastDisplayNames = computed(() => {
  const h = displayNameHistory.value;
  if (!h) return [];
  const names = new Map(); // name -> latest time
  const histories = h.history || {};
  for (const [groupId, nameMap] of Object.entries(histories)) {
    for (const [name, ts] of Object.entries(nameMap || {})) {
      const prev = names.get(name);
      if (!prev || new Date(ts) > new Date(prev)) names.set(name, ts);
    }
  }
  const arr = Array.from(names.entries())
    .sort((a, b) => new Date(b[1]) - new Date(a[1]))
    .map(([name]) => name);
  return arr.slice(0, 10);
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
</script>

<style scoped></style>
