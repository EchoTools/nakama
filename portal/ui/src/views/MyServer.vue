<template>
  <div class="max-w-7xl mx-auto mt-10 px-10 text-gray-100">
    <div class="flex items-center justify-between mb-6">
      <div class="flex items-center gap-4">
        <span class="text-3xl leading-none">🟢</span>
        <h2 class="text-3xl font-semibold">My Server</h2>
      </div>
      <div class="flex items-center gap-3">
        <div class="hidden sm:flex items-center rounded-lg border border-slate-600/50 overflow-hidden">
          <button
            type="button"
            class="px-3 py-1.5 text-sm font-semibold transition"
            :class="viewMode === 'current' ? 'bg-slate-700 text-slate-100' : 'bg-slate-900/40 text-slate-300 hover:bg-slate-800'"
            @click="setViewMode('current')"
          >
            Current Session
          </button>
          <button
            type="button"
            class="px-3 py-1.5 text-sm font-semibold transition border-l border-slate-600/50"
            :class="viewMode === 'owned' ? 'bg-slate-700 text-slate-100' : 'bg-slate-900/40 text-slate-300 hover:bg-slate-800'"
            @click="setViewMode('owned')"
          >
            Owned Servers
          </button>
        </div>
        <span v-if="userState.profile" class="text-xs text-gray-400">
          {{ userState.profile.displayName || userState.profile.username }}
        </span>
        <button
          type="button"
          class="inline-flex items-center gap-2 px-3 py-1.5 rounded bg-slate-700 hover:bg-slate-600 text-slate-100 border border-slate-500/40 text-sm font-semibold transition disabled:opacity-60 disabled:cursor-not-allowed"
          :disabled="loading"
          @click="refresh"
        >
          🔄 Refresh
        </button>
      </div>
    </div>

    <div v-if="error" class="text-red-400 mb-4">{{ error }}</div>

    <div v-if="loading" class="bg-slate-800 border border-slate-600/50 rounded-xl p-5 shadow-sm shadow-black/30">
      <div class="text-gray-400">Loading servers...</div>
    </div>

    <div v-else>
      <div v-if="servers.length === 0" class="text-gray-100/80">No servers found.</div>

      <div v-else>
        <div v-if="viewMode === 'current'" class="text-xs text-gray-400 mb-3">
          Current Session matches your logged-in account (Discord → Nakama user ID) against the match label players list.
        </div>

        <div class="space-y-6">
          <div
            v-for="server in servers"
            :key="server.match_id"
            class="bg-slate-800 border border-slate-600/50 rounded-xl p-5 shadow-sm shadow-black/30"
          >
          <div class="flex items-start justify-between gap-4">
            <div>
              <div class="flex items-center gap-2">
                <span :class="['px-2 py-0.5 text-xs font-semibold rounded', getModeClass(server.label?.mode)]">
                  {{ formatMode(server.label?.mode) }}
                </span>
                <span
                  v-if="lobbyTypeBadge(server.label)"
                  :class="['px-2 py-0.5 text-xs font-semibold rounded', lobbyTypeBadgeClass(server.label)]"
                >
                  {{ lobbyTypeBadge(server.label) }}
                </span>
                <span
                  :class="[
                    'px-2 py-0.5 text-xs font-semibold rounded',
                    server.label?.open ? 'bg-green-900 text-green-200' : 'bg-red-900 text-red-200',
                  ]"
                >
                  {{ server.label?.open ? 'OPEN' : 'CLOSED' }}
                </span>
                <span class="text-xs text-gray-500 font-mono">{{ server.match_id?.slice(0, 8) }}…</span>
              </div>
              <div class="text-sm text-gray-400 mt-1">
                Guild:
                <span class="text-gray-200">{{ serverGuildId(server) || '—' }}</span>
                <span class="text-gray-600">•</span>
                Players:
                <span class="text-gray-200">{{ server.label?.player_count || 0 }}</span>
                <template v-if="serverLocation(server)">
                  <span class="text-gray-600">•</span>
                  Location:
                  <span class="text-gray-200">{{ serverLocation(server) }}</span>
                </template>
                <template v-if="server.label?.broadcaster?.endpoint">
                  <span class="text-gray-600">•</span>
                  Endpoint:
                  <span class="text-gray-200 font-mono">{{ formatEndpoint(server.label.broadcaster.endpoint) }}</span>
                </template>
              </div>
            </div>

            <div class="flex items-center gap-2">
              <button
                v-if="viewMode === 'current' && shouldShowTaxi(server)"
                type="button"
                class="px-3 py-1.5 rounded bg-sky-700/70 hover:bg-sky-600 border border-slate-600/50 text-slate-100 text-sm font-semibold"
                title="Copy Echo Taxi link"
                @click="handleTaxiCopy(server)"
              >
                📋 Copy Taxi
              </button>
              <button
                v-if="viewMode === 'owned' && shouldShowTaxi(server)"
                type="button"
                class="px-3 py-1.5 rounded bg-sky-700/70 hover:bg-sky-600 border border-slate-600/50 text-slate-100 text-sm font-semibold"
                title="Copy & open Echo Taxi link"
                @click="handleTaxiJoin(server)"
              >
                🚕 Join Taxi
              </button>
              <button
                type="button"
                class="px-3 py-1.5 rounded bg-slate-700 hover:bg-slate-600 border border-slate-600/50 text-slate-100 text-sm font-semibold"
                @click="openReport(server)"
              >
                📝 Report
              </button>
              <button
                type="button"
                class="px-3 py-1.5 rounded bg-slate-700 hover:bg-slate-600 border border-slate-600/50 text-slate-100 text-sm font-semibold"
                @click="openShutdown(server)"
              >
                ⚠️ Shutdown
              </button>
            </div>
          </div>

          <div class="mt-4 border-t border-slate-700/60 pt-4">
            <div v-if="serverScoreParts(server)" class="mb-3">
              <div class="inline-flex items-center gap-3 rounded-lg bg-slate-900/60 border border-slate-700/60 px-3 py-2">
                <div v-if="serverScoreParts(server)" class="flex items-baseline gap-2">
                  <span class="text-xs text-slate-400">Score</span>
                  <span class="text-base font-semibold font-mono leading-none">
                    <span class="text-blue-300">BLUE {{ serverScoreParts(server).blue }}</span>
                    <span class="text-slate-500 mx-1">-</span>
                    <span class="text-orange-300">ORANGE {{ serverScoreParts(server).orange }}</span>
                  </span>
                </div>
              </div>
            </div>
            <div class="text-sm text-gray-300 mb-2">Players</div>

            <div v-if="!hasAnyPlayers(server.label?.players)" class="text-gray-400 text-sm">No players listed.</div>

            <div v-else class="space-y-4">
              <div v-for="group in groupedPlayers(server.label.players)" :key="group.key" v-show="group.players.length">
                <div class="flex items-center justify-between mb-2">
                  <div class="text-xs font-semibold text-gray-300 tracking-wide">
                    {{ group.title }}
                    <span class="text-gray-500 font-normal">({{ group.count ?? group.players.length }})</span>
                  </div>
                </div>

                <div class="space-y-2">
                  <button
                    v-for="(p, idx) in group.players"
                    :key="playerKey(p, idx)"
                    type="button"
                    class="w-full flex items-center justify-between gap-3 px-3 py-2 rounded bg-slate-900/70 hover:bg-slate-700/60 border border-slate-700/60 text-sm transition text-left"
                    @click="openKick(server, p)"
                  >
                    <div class="flex items-center gap-2 min-w-0">
                      <span :class="['shrink-0 px-2 py-0.5 text-[11px] font-semibold rounded', teamBadgeClass(p)]">
                        {{ teamLabel(p) }}
                      </span>

                      <div
                        class="h-8 w-8 rounded-full bg-slate-900/60 border border-slate-700/60 overflow-hidden flex items-center justify-center shrink-0"
                        :title="playerAvatarTitle(p)"
                      >
                        <img
                          v-if="playerAvatarUrl(p)"
                          :src="playerAvatarUrl(p)"
                          :alt="playerAvatarAlt(p)"
                          class="h-full w-full object-cover"
                          loading="lazy"
                          referrerpolicy="no-referrer"
                          @error="markPlayerAvatarError(p)"
                        />
                        <span v-else class="text-slate-200 font-semibold text-xs select-none">{{ playerAvatarFallback(p) }}</span>
                      </div>

                      <div class="min-w-0">
                        <div class="font-medium text-slate-100 truncate leading-tight">
                          {{ playerPrimaryName(p) }}
                        </div>
                        <div v-if="playerSecondaryName(p)" class="text-[11px] text-slate-400 font-mono truncate leading-tight">
                          {{ playerSecondaryName(p) }}
                        </div>
                        <div v-if="p.__portalGhost" class="text-[11px] text-yellow-300/90 leading-tight">
                          Pending: awaiting authoritative refresh
                        </div>
                      </div>
                    </div>

                    <div class="flex items-center gap-2 shrink-0">
                      <span v-if="formatPing(p)" class="text-xs text-slate-300">{{ formatPing(p) }}</span>
                      <span v-if="p.user_id" class="text-xs text-slate-400 font-mono">{{ p.user_id.slice(0, 8) }}…</span>
                    </div>
                  </button>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
    </div>

    <!-- Kick / Suspend Modal -->
    <div v-if="kickModalOpen" class="fixed inset-0 z-50 flex items-center justify-center">
      <div class="absolute inset-0 bg-black/60" @click="closeKick"></div>
      <div class="relative w-full max-w-xl bg-slate-800 border border-slate-600/50 rounded-xl shadow-lg">
        <div class="flex items-center justify-between px-5 py-4 border-b border-slate-700/60">
          <h3 class="text-lg font-semibold">🚫 Kick / Suspend Player</h3>
          <button class="text-gray-400 hover:text-gray-200" @click="closeKick">✕</button>
        </div>

        <form class="px-5 py-4 space-y-4" @submit.prevent="submitKick">
          <div>
            <div class="text-xs text-gray-400 mb-1">Player</div>
            <div class="px-3 py-2 rounded bg-slate-900/50 border border-slate-700/60">
              {{ selectedPlayerName }}
            </div>
          </div>

          <div>
            <label class="block text-sm text-gray-300 mb-1">Reason</label>
            <select v-model="kickForm.reason" class="w-full rounded bg-slate-900/60 border border-slate-700/60 px-3 py-2">
              <optgroup label="1. Zero Tolerance for Hacks">
                <option value="hacks">Using hacks, cheats, or exploits.</option>
              </optgroup>
              <optgroup label="2. Respectful Behavior">
                <option value="disrespect">Disrespectful or unkind behavior.</option>
                <option value="prejudice">Prejudiced remarks.</option>
                <option value="explicit">Sexually explicit comments.</option>
                <option value="discrimination">Sexist/racist/discriminatory language.</option>
                <option value="disruptive">Disruptive or problematic behavior.</option>
                <option value="sensitive">Provoking sensitive topics.</option>
              </optgroup>
              <optgroup label="3. Harassment / Bullying">
                <option value="harassment">Harassment, bullying, or threats.</option>
              </optgroup>
              <optgroup label="4. Fair Play">
                <option value="exploits">Exploiting mechanics or play space.</option>
                <option value="unsportsmanlike">Unsportsmanlike conduct.</option>
                <option value="trolling">Trolling / disruptive play.</option>
              </optgroup>
              <optgroup label="5. Violence / Illegal Activity">
                <option value="violence">Promoting violence or illegal activity.</option>
              </optgroup>
              <optgroup label="6. Personal Information">
                <option value="callout">Public call-outs instead of proper channels.</option>
                <option value="doxxing">Sharing personal information.</option>
              </optgroup>
            </select>
          </div>

          <div>
            <label class="block text-sm text-gray-300 mb-1">Action Type</label>
            <select v-model="kickForm.action" class="w-full rounded bg-slate-900/60 border border-slate-700/60 px-3 py-2">
              <option value="kick">Kick (temporary removal)</option>
              <option value="suspend">Suspend (longer-term restriction)</option>
            </select>
            <div v-if="kickForm.action === 'suspend'" class="mt-2 text-xs text-yellow-300">
              Suspend is not wired up yet in portal (kick works).
            </div>
          </div>

          <div v-if="kickForm.action === 'suspend'" class="grid grid-cols-2 gap-3">
            <div>
              <label class="block text-sm text-gray-300 mb-1">Suspension Length</label>
              <input
                v-model.number="kickForm.suspension_value"
                type="number"
                min="1"
                class="w-full rounded bg-slate-900/60 border border-slate-700/60 px-3 py-2"
              />
            </div>
            <div>
              <label class="block text-sm text-gray-300 mb-1">Unit</label>
              <select v-model="kickForm.suspension_unit" class="w-full rounded bg-slate-900/60 border border-slate-700/60 px-3 py-2">
                <option value="minutes">Minutes</option>
                <option value="hours">Hours</option>
                <option value="days">Days</option>
                <option value="weeks">Weeks</option>
              </select>
            </div>
          </div>

          <div class="flex items-center gap-2">
            <input id="escalate" v-model="kickForm.escalate" type="checkbox" class="accent-sky-500" />
            <label for="escalate" class="text-sm text-gray-300">🚨 Escalate to Community Values</label>
          </div>

          <div v-if="kickForm.action === 'suspend'" class="flex items-center gap-2">
            <input id="allowPrivates" v-model="kickForm.allow_privates" type="checkbox" class="accent-sky-500" />
            <label for="allowPrivates" class="text-sm text-gray-300">🔏 Allow privates</label>
          </div>

          <div>
            <label class="block text-sm text-gray-300 mb-1">Additional Comments</label>
            <textarea
              v-model="kickForm.comments"
              rows="2"
              class="w-full rounded bg-slate-900/60 border border-slate-700/60 px-3 py-2"
            ></textarea>
          </div>

          <div
            v-if="kickStatus"
            class="text-sm whitespace-pre-wrap"
            :class="
              kickStatusKind === 'error'
                ? 'text-red-400'
                : kickStatusKind === 'warning'
                  ? 'text-yellow-300'
                  : 'text-green-400'
            "
          >
            {{ kickStatus }}
          </div>

          <div class="flex justify-end gap-3 pt-2">
            <button
              type="button"
              class="px-4 py-2 rounded bg-slate-700 hover:bg-slate-600 border border-slate-600/50 text-slate-100"
              @click="closeKick"
            >
              Cancel
            </button>
            <button
              v-if="lastKickAttempt"
              type="button"
              class="px-4 py-2 rounded bg-slate-700 hover:bg-slate-600 border border-slate-600/50 text-slate-100 disabled:opacity-60 disabled:cursor-not-allowed"
              :disabled="kickSubmitting"
              @click="retryLastKick"
            >
              ↻ Retry Kick
            </button>
            <button
              type="submit"
              class="px-4 py-2 rounded bg-red-600 hover:bg-red-500 text-white font-semibold disabled:opacity-60 disabled:cursor-not-allowed"
              :disabled="kickSubmitting"
            >
              Confirm Action
            </button>
          </div>
        </form>
      </div>
    </div>

    <!-- Shutdown Modal -->
    <div v-if="shutdownModalOpen" class="fixed inset-0 z-50 flex items-center justify-center">
      <div class="absolute inset-0 bg-black/60" @click="closeShutdown"></div>
      <div class="relative w-full max-w-xl bg-slate-800 border border-slate-600/50 rounded-xl shadow-lg">
        <div class="flex items-center justify-between px-5 py-4 border-b border-slate-700/60">
          <h3 class="text-lg font-semibold">⚠️ Shutdown Server</h3>
          <button class="text-gray-400 hover:text-gray-200" @click="closeShutdown">✕</button>
        </div>

        <form class="px-5 py-4 space-y-4" @submit.prevent="submitShutdown">
          <div>
            <label class="block text-sm text-gray-300 mb-1">Reason</label>
            <select v-model="shutdownForm.reason" class="w-full rounded bg-slate-900/60 border border-slate-700/60 px-3 py-2">
              <option value="">-- Select a reason --</option>
              <option value="stuck">🛑 Match stuck</option>
              <option value="empty">👥 Empty server</option>
              <option value="crash">💥 Crashed / unresponsive</option>
              <option value="lag">🐢 Severe lag</option>
              <option value="other">✍️ Other</option>
            </select>
          </div>

          <div v-if="shutdownForm.reason === 'other'">
            <label class="block text-sm text-gray-300 mb-1">Custom Reason</label>
            <textarea
              v-model="shutdownForm.custom_reason"
              rows="2"
              class="w-full rounded bg-slate-900/60 border border-slate-700/60 px-3 py-2"
            ></textarea>
          </div>

          <div>
            <label class="block text-sm text-gray-300 mb-1">Grace Period?</label>
            <select v-model="shutdownForm.use_grace" class="w-full rounded bg-slate-900/60 border border-slate-700/60 px-3 py-2">
              <option value="no">No</option>
              <option value="yes">Yes</option>
            </select>
          </div>

          <div v-if="shutdownForm.use_grace === 'yes'" class="grid grid-cols-2 gap-3">
            <div>
              <label class="block text-sm text-gray-300 mb-1">Grace Value</label>
              <input
                v-model.number="shutdownForm.grace_value"
                type="number"
                min="1"
                class="w-full rounded bg-slate-900/60 border border-slate-700/60 px-3 py-2"
              />
            </div>
            <div>
              <label class="block text-sm text-gray-300 mb-1">Unit</label>
              <select v-model="shutdownForm.grace_unit" class="w-full rounded bg-slate-900/60 border border-slate-700/60 px-3 py-2">
                <option value="seconds">Seconds</option>
                <option value="minutes">Minutes</option>
                <option value="hours">Hours</option>
              </select>
            </div>
          </div>

          <div v-if="shutdownStatus" class="text-sm" :class="shutdownStatusKind === 'error' ? 'text-red-400' : 'text-green-400'">
            {{ shutdownStatus }}
          </div>

          <div class="flex justify-end gap-3 pt-2">
            <button
              type="button"
              class="px-4 py-2 rounded bg-slate-700 hover:bg-slate-600 border border-slate-600/50 text-slate-100"
              @click="closeShutdown"
            >
              Cancel
            </button>
            <button
              type="submit"
              class="px-4 py-2 rounded bg-yellow-500 hover:bg-yellow-400 text-black font-semibold disabled:opacity-60 disabled:cursor-not-allowed"
              :disabled="shutdownSubmitting"
            >
              Confirm Shutdown
            </button>
          </div>
        </form>
      </div>
    </div>

    <!-- Report / Feedback Modal -->
    <div v-if="reportModalOpen" class="fixed inset-0 z-50 flex items-center justify-center">
      <div class="absolute inset-0 bg-black/60" @click="closeReport"></div>
      <div class="relative w-full max-w-xl bg-slate-800 border border-slate-600/50 rounded-xl shadow-lg">
        <div class="flex items-center justify-between px-5 py-4 border-b border-slate-700/60">
          <h3 class="text-lg font-semibold">📝 Report / Feedback</h3>
          <button class="text-gray-400 hover:text-gray-200" @click="closeReport">✕</button>
        </div>

        <form class="px-5 py-4 space-y-4" @submit.prevent="submitReport">
          <div>
            <label class="block text-sm text-gray-300 mb-1">Issue Type</label>
            <select v-model="reportForm.issue_type" class="w-full rounded bg-slate-900/60 border border-slate-700/60 px-3 py-2">
              <option value="server_issue">Server issue</option>
              <option value="player_report">Player report</option>
              <option value="feedback">Feedback / suggestion</option>
              <option value="bug">Bug</option>
            </select>
          </div>

          <div>
            <label class="block text-sm text-gray-300 mb-1">Severity</label>
            <select v-model="reportForm.severity" class="w-full rounded bg-slate-900/60 border border-slate-700/60 px-3 py-2">
              <option value="low">Low</option>
              <option value="medium">Medium</option>
              <option value="high">High</option>
              <option value="critical">Critical</option>
            </select>
          </div>

          <div>
            <label class="block text-sm text-gray-300 mb-1">Details</label>
            <textarea
              v-model="reportForm.details"
              rows="4"
              class="w-full rounded bg-slate-900/60 border border-slate-700/60 px-3 py-2"
              placeholder="Describe the issue..."
            ></textarea>
          </div>

          <div class="text-xs text-gray-400">
            Reporting is UI-only for now (no portal RPC found yet). Submit will copy a JSON payload to your clipboard.
          </div>

          <div v-if="reportStatus" class="text-sm" :class="reportStatusKind === 'error' ? 'text-red-400' : 'text-green-400'">
            {{ reportStatus }}
          </div>

          <div class="flex justify-end gap-3 pt-2">
            <button
              type="button"
              class="px-4 py-2 rounded bg-slate-700 hover:bg-slate-600 border border-slate-600/50 text-slate-100"
              @click="closeReport"
            >
              Cancel
            </button>
            <button
              type="submit"
              class="px-4 py-2 rounded bg-sky-600 hover:bg-sky-500 text-white font-semibold"
              :disabled="reportSubmitting"
            >
              Submit Report
            </button>
          </div>
        </form>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue';
import { get as apiGet, callRpc } from '../lib/apiClient.js';
import { userState } from '../composables/useUserState.js';

const ZERO_UUID = '00000000-0000-0000-0000-000000000000';

const apiBaseRaw = import.meta.env.VITE_NAKAMA_API_BASE;
const apiBaseDisplay = computed(() => {
  try {
    const u = new URL(apiBaseRaw);
    return `${u.hostname}:${u.port || (u.protocol === 'https:' ? '443' : '80')}`;
  } catch (_) {
    return String(apiBaseRaw || '').replace(/^https?:\/\//, '').replace(/\/$/, '');
  }
});

const servers = ref([]);
const loading = ref(false);
const error = ref('');

// Avatar cache (by Nakama user_id)
const avatarsByUserId = ref(new Map());
const avatarErrorUserIds = ref(new Set());
const discordIdsByUserId = ref(new Map());

const viewMode = ref('current'); // 'current' | 'owned'

const kickModalOpen = ref(false);
const kickSubmitting = ref(false);
const selectedServer = ref(null);
const selectedPlayer = ref(null);
const kickStatus = ref('');
const kickStatusKind = ref('success'); // 'success' | 'warning' | 'error'
const lastKickAttempt = ref(null);

// UI-only grace window: if a kick attempt makes a player disappear from the match label briefly
// (label lag or immediate reconnect), keep them visible as "Pending" for a short time.
const KICK_GHOST_WINDOW_MS = 20_000;
const recentKickTargetsByMatchId = new Map();
const lastLabelSnapshotByMatchId = new Map();

const kickForm = ref({
  reason: 'disrespect',
  action: 'kick',
  suspension_value: 1,
  suspension_unit: 'days',
  escalate: false,
  allow_privates: false,
  comments: '',
});

const shutdownModalOpen = ref(false);
const shutdownSubmitting = ref(false);
const shutdownServer = ref(null);
const shutdownStatus = ref('');
const shutdownStatusKind = ref('success');

const shutdownForm = ref({
  reason: '',
  custom_reason: '',
  use_grace: 'no',
  grace_value: 5,
  grace_unit: 'minutes',
});

const reportModalOpen = ref(false);
const reportSubmitting = ref(false);
const reportServer = ref(null);
const reportStatus = ref('');
const reportStatusKind = ref('success');

const reportForm = ref({
  issue_type: 'server_issue',
  severity: 'medium',
  details: '',
});

const selectedPlayerName = computed(() => {
  const p = selectedPlayer.value;
  if (!p) return '—';
  const display = displayNameForPlayer(p);
  const discord = discordUsernameForPlayer(p);
  if (display && discord) return `${display} (${discord})`;
  return display || discord || p.user_id || '—';
});

async function resolvePlayerUserId(player) {
  if (!player || typeof player !== 'object') return null;

  const existing = player.user_id || player.userId;
  if (existing) return String(existing);

  // Try resolving via account/lookup using whatever identifiers we have.
  const discordId = player.discord_id || player.discordId;
  const xpId = player.xp_id || player.xpId || player.evr_id || player.evrId;
  const username = player.username;

  let lastErr = null;

  async function tryLookup(body) {
    const res = await callRpc('account/lookup', body);
    const data = await readResponsePayload(res);
    if (!res.ok) {
      lastErr = formatRpcFailure('account/lookup', res, data);
      return null;
    }
    const id = data?.id;
    return id ? String(id) : null;
  }

  if (discordId) {
    const id = await tryLookup({ discord_id: String(discordId) });
    if (id) return id;
  }

  // xp_id must be a parsable EVR ID string on the server; evr_id often serializes that way.
  if (xpId) {
    const id = await tryLookup({ xp_id: String(xpId) });
    if (id) return id;
  }

  if (username) {
    const id = await tryLookup({ username: String(username) });
    if (id) return id;
  }

  if (lastErr) throw new Error(lastErr);
  return null;
}

async function readResponsePayload(res) {
  try {
    const text = await res.text();
    if (!text) return {};
    try {
      return JSON.parse(text);
    } catch (_) {
      return { raw: text };
    }
  } catch (_) {
    return {};
  }
}

function formatRpcFailure(rpcId, res, payload) {
  const status = res?.status;
  const code = payload?.code;
  const message = payload?.message || payload?.error || payload?.raw;
  const hint =
    status === 401
      ? 'Auth failed (JWT expired/invalid?)'
      : status === 403
        ? 'Forbidden (not a moderator/enforcer?)'
        : status === 404
          ? 'RPC not found on this endpoint'
          : null;

  const parts = [`${rpcId} failed (HTTP ${status || '?'})`];
  if (typeof code !== 'undefined') parts.push(`code: ${code}`);
  if (message) parts.push(String(message));
  if (hint) parts.push(`hint: ${hint}`);
  return parts.join('\n');
}

function parseMatchLabel(match) {
  try {
    const raw = match?.label?.value ?? match?.label;
    if (!raw) return null;
    if (typeof raw === 'string') return JSON.parse(raw);
    if (typeof raw === 'object') return raw;
    return null;
  } catch (_) {
    return null;
  }
}

function isSafeAvatarUrl(url) {
  const s = String(url || '').trim();
  if (!s) return false;
  if (s.startsWith('data:image/')) return true;
  try {
    const u = new URL(s);
    return u.protocol === 'https:' || u.protocol === 'http:';
  } catch (_) {
    return false;
  }
}

function metadataObjectFromAny(obj) {
  if (!obj || typeof obj !== 'object') return null;
  const m = obj.metadata;
  if (!m) return null;
  if (typeof m === 'object') return m;
  if (typeof m === 'string') {
    try {
      const parsed = JSON.parse(m);
      return parsed && typeof parsed === 'object' ? parsed : null;
    } catch (_) {
      return null;
    }
  }
  return null;
}

function normalizeAvatarUrlCandidate(url) {
  const s = String(url || '').trim();
  if (!s) return '';
  if (s.startsWith('//')) return `https:${s}`;
  if (s.startsWith('cdn.discordapp.com/')) return `https://${s}`;
  if (s.startsWith('media.discordapp.net/')) return `https://${s}`;
  return s;
}

function discordIdFromObj(obj) {
  if (!obj || typeof obj !== 'object') return '';
  const m = metadataObjectFromAny(obj);
  const raw =
    obj.discord_id ??
    obj.discordId ??
    obj.custom_id ??
    obj.customId ??
    obj.customID ??
    m?.discord_id ??
    m?.discordId ??
    m?.custom_id ??
    m?.customId ??
    m?.customID ??
    '';
  return String(raw || '').trim();
}

function looksLikeDiscordAvatarHash(s) {
  const v = String(s || '').trim();
  if (!v) return false;
  if (v.startsWith('a_')) return /^[a-z0-9_]+$/i.test(v) && v.length >= 5;
  return /^[a-z0-9]+$/i.test(v) && v.length >= 10;
}

function discordAvatarCdnUrl(discordId, avatarHash, size = 128) {
  const did = String(discordId || '').trim();
  const hash = String(avatarHash || '').trim();
  if (!did || !hash) return '';
  const ext = hash.startsWith('a_') ? 'gif' : 'png';
  const qs = `size=${encodeURIComponent(String(size))}`;
  return `https://cdn.discordapp.com/avatars/${encodeURIComponent(did)}/${encodeURIComponent(hash)}.${ext}?${qs}`;
}

function avatarUrlFromAny(obj, fallbackDiscordId) {
  if (!obj || typeof obj !== 'object') return '';
  const m = metadataObjectFromAny(obj);
  const raw =
    obj.avatar_url ??
    obj.avatarUrl ??
    obj.avatarURL ??
    m?.avatar_url ??
    m?.avatarUrl ??
    m?.avatarURL ??
    '';
  const s = normalizeAvatarUrlCandidate(raw);

  if (isSafeAvatarUrl(s)) return s;

  const did = discordIdFromObj(obj) || String(fallbackDiscordId || '').trim();
  if (did && looksLikeDiscordAvatarHash(s)) {
    return discordAvatarCdnUrl(did, s, 128);
  }

  return '';
}

function avatarFallbackFromName(name) {
  const primary = String(name || '').trim();
  if (!primary) return '?';
  const parts = primary.split(/\s+/).filter(Boolean);
  const first = parts[0] || '';
  const second = parts.length > 1 ? parts[1] : '';
  const out = (first.slice(0, 1) + second.slice(0, 1)).toUpperCase();
  return out || primary.slice(0, 1).toUpperCase() || '?';
}

function normalizeUserId(value) {
  const s = String(value || '').trim();
  return s ? s.toLowerCase() : '';
}

function chunkArray(arr, size) {
  const out = [];
  for (let i = 0; i < arr.length; i += size) out.push(arr.slice(i, i + size));
  return out;
}

async function prefetchAvatarsForUserIds(userIds, discordIdByUserId) {
  const ids = Array.from(new Set(userIds.map((x) => String(x || '').trim()).filter(Boolean)));
  if (ids.length === 0) return;

  const batches = chunkArray(ids, 50);
  for (const batch of batches) {
    try {
      const qs = batch.map((id) => `ids=${encodeURIComponent(id)}`).join('&');
      const res = await apiGet(`/user?${qs}`);
      if (!res.ok) continue;
      const data = await res.json().catch(() => ({}));
      const users = Array.isArray(data?.users) ? data.users : [];
      if (!users.length) continue;

      const next = new Map(avatarsByUserId.value);
      for (const u of users) {
        const uid = String(u?.id || '').trim();
        if (!uid) continue;
        const k = normalizeUserId(uid);
        const fallbackDid = discordIdByUserId && typeof discordIdByUserId.get === 'function' ? discordIdByUserId.get(k) : '';
        const url = avatarUrlFromAny(u, fallbackDid);
        if (!url) continue;
        next.set(k, url);
      }
      avatarsByUserId.value = next;
    } catch (_) {
      // Non-fatal.
    }
  }
}

function playerAvatarUrl(player) {
  if (!player || typeof player !== 'object') return '';

  // Prefer any avatar in the match label itself.
  const direct = avatarUrlFromAny(player);
  if (direct) return direct;

  const uid = normalizeUserId(player.user_id || player.userId);
  if (!uid || uid === ZERO_UUID) return '';
  if (avatarErrorUserIds.value.has(uid)) return '';
  return avatarsByUserId.value.get(uid) || '';
}

function playerAvatarFallback(player) {
  return avatarFallbackFromName(playerPrimaryName(player) || playerSecondaryName(player) || '');
}

function playerAvatarAlt(player) {
  const name = playerPrimaryName(player) || playerSecondaryName(player) || 'Player';
  return `${name} avatar`;
}

function playerAvatarTitle(player) {
  const name = playerPrimaryName(player) || playerSecondaryName(player) || '';
  return name ? name : 'Player';
}

function markPlayerAvatarError(player) {
  const uid = normalizeUserId(player?.user_id || player?.userId);
  if (!uid || uid === ZERO_UUID) return;
  const next = new Set(avatarErrorUserIds.value);
  next.add(uid);
  avatarErrorUserIds.value = next;
}

function cloneJson(value) {
  try {
    return value ? JSON.parse(JSON.stringify(value)) : value;
  } catch (_) {
    return value;
  }
}

function identityKeysForPlayer(player) {
  if (!player || typeof player !== 'object') return [];
  const keys = [];

  const userId = player.user_id || player.userId || player.nakama_id || player.nakamaId || player.id;
  const discordId = player.discord_id || player.discordId;
  const sessionId = player.session_id || player.sessionId;
  const username = player.username;
  const display = player.display_name ?? player.displayName;

  if (userId) keys.push(`u:${String(userId)}`);
  if (discordId) keys.push(`d:${String(discordId)}`);
  if (username) keys.push(`n:${String(username)}`);
  if (display) keys.push(`dn:${String(display)}`);
  if (sessionId) keys.push(`s:${String(sessionId)}`);

  return keys;
}

function rememberKickAttempt(matchId, player) {
  if (!matchId || !player) return;
  const now = Date.now();
  const entry = recentKickTargetsByMatchId.get(matchId) || { expiresAt: 0, targetKeys: new Set() };
  for (const k of identityKeysForPlayer(player)) entry.targetKeys.add(k);
  entry.expiresAt = Math.max(entry.expiresAt, now + KICK_GHOST_WINDOW_MS);
  recentKickTargetsByMatchId.set(matchId, entry);
}

function applyKickGhosts(matchId, label) {
  if (!matchId || !label || typeof label !== 'object') return;
  const entry = recentKickTargetsByMatchId.get(matchId);
  if (!entry || entry.expiresAt < Date.now()) return;

  const prev = lastLabelSnapshotByMatchId.get(matchId);
  const prevPlayers = Array.isArray(prev?.players) ? prev.players : [];
  if (prevPlayers.length === 0) return;

  const currentPlayers = Array.isArray(label.players) ? label.players : [];
  const currentKeys = new Set();
  for (const p of currentPlayers) {
    for (const k of identityKeysForPlayer(p)) currentKeys.add(k);
  }

  const merged = [...currentPlayers];
  for (const p of prevPlayers) {
    const keys = identityKeysForPlayer(p);
    if (keys.length === 0) continue;
    const isTarget = keys.some((k) => entry.targetKeys.has(k));
    if (!isTarget) continue;
    const alreadyPresent = keys.some((k) => currentKeys.has(k));
    if (alreadyPresent) continue;
    merged.push({ ...(p || {}), __portalGhost: true });
  }

  label.players = merged;
}

function toFiniteNumber(value) {
  if (value === null || typeof value === 'undefined') return null;
  const n = typeof value === 'string' ? Number(value) : Number(value);
  return Number.isFinite(n) ? n : null;
}

function gameStateForLabel(label) {
  if (!label || typeof label !== 'object') return null;
  return label.game_state || label.gameState || null;
}

function serverScoreParts(server) {
  const label = server?.label;
  const gs = gameStateForLabel(label);
  if (!gs) return null;

  const blue = toFiniteNumber(gs.blue_score ?? gs.blueScore);
  const orange = toFiniteNumber(gs.orange_score ?? gs.orangeScore);
  if (blue === null && orange === null) return null;
  return {
    blue: blue ?? 0,
    orange: orange ?? 0,
  };
}

function serverGuildId(server) {
  const label = server?.label;
  return label?.group_id || label?.groupId || label?.guild_id || label?.guildId || null;
}

async function refresh() {
  await fetchMyServers();
}

async function setViewMode(mode) {
  if (mode !== 'current' && mode !== 'owned') return;
  if (viewMode.value === mode) return;
  viewMode.value = mode;
  await fetchMyServers();
}

function labelHasUser(label, ids) {
  if (!label || !ids || typeof ids !== 'object') return false;
  const userId = ids.userId ? String(ids.userId) : '';
  const discordId = ids.discordId ? String(ids.discordId) : '';

  if (!userId && !discordId) return false;

  const players = Array.isArray(label.players) ? label.players : [];
  return players.some((p) => {
    if (!p || typeof p !== 'object') return false;

    const pid = p.user_id || p.userId || p.nakama_id || p.nakamaId || p.id;
    if (userId && pid && String(pid) === userId) return true;

    const did = p.discord_id || p.discordId;
    if (discordId && did && String(did) === discordId) return true;

    return false;
  });
}

async function fetchMyServers() {
  loading.value = true;
  error.value = '';

  try {
    // Include a cache-busting query param so values like ping_ms don't get stuck behind a cached GET.
    const res = await apiGet(`/match?limit=100&authoritative=true&_ts=${Date.now()}`, { cache: 'no-store' });
    if (!res.ok) throw new Error('Failed to fetch matches.');

    const data = await res.json();
    const matches = data.matches || [];

    const nextSnapshot = new Map();

    const myUserId = userState.profile?.nakamaId;
    const myDiscordId = userState.profile?.id;
    const out = [];

    for (const match of matches) {
      const label = parseMatchLabel(match);
      if (!label || typeof label !== 'object') continue;

      // Normalize players array.
      if (!Array.isArray(label.players)) label.players = [];
      if (typeof label.player_count !== 'number') label.player_count = label.players.length;

      // Store a raw snapshot (no ghosts) for the next refresh.
      nextSnapshot.set(match.match_id, cloneJson(label));

      // UI-only: keep recently-kicked targets visible briefly.
      applyKickGhosts(match.match_id, label);

      const operatorId = label.broadcaster?.oper || label.broadcaster?.operator_id || label.game_server?.operator_id;
      const isOwned = myUserId && operatorId === myUserId;
      const isCurrentSession = (myUserId || myDiscordId) && labelHasUser(label, { userId: myUserId, discordId: myDiscordId });

      const include = viewMode.value === 'owned' ? isOwned : isCurrentSession;
      if (include) {
        out.push({
          match_id: match.match_id,
          label,
        });
      }
    }

    servers.value = out;

    // Best-effort: prefetch avatars for all visible players.
    const ids = [];
    const didByUid = new Map(discordIdsByUserId.value);
    for (const s of out) {
      const players = Array.isArray(s?.label?.players) ? s.label.players : [];
      for (const p of players) {
        const uidRaw = p?.user_id || p?.userId;
        const uid = String(uidRaw || '').trim();
        if (!uid) continue;
        if (uid.toLowerCase() === ZERO_UUID) continue;
        const k = uid.toLowerCase();
        const did = String(p?.discord_id || p?.discordId || '').trim();
        if (did) didByUid.set(k, did);
        if (avatarsByUserId.value.has(k) || avatarErrorUserIds.value.has(k)) continue;
        ids.push(uid);
      }
    }
    discordIdsByUserId.value = didByUid;
    void prefetchAvatarsForUserIds(ids, discordIdsByUserId.value);

    lastLabelSnapshotByMatchId.clear();
    for (const [k, v] of nextSnapshot.entries()) lastLabelSnapshotByMatchId.set(k, v);
  } catch (e) {
    error.value = e?.message || 'Failed to load servers.';
    servers.value = [];
  } finally {
    loading.value = false;
  }
}

function openKick(server, player) {
  selectedServer.value = server;
  selectedPlayer.value = player;
  kickStatus.value = '';
  kickStatusKind.value = 'success';
  lastKickAttempt.value = null;
  kickModalOpen.value = true;
}

function closeKick() {
  kickModalOpen.value = false;
  selectedServer.value = null;
  selectedPlayer.value = null;
  kickSubmitting.value = false;
  lastKickAttempt.value = null;
}

async function fetchMatchLabelById(matchId) {
  const res = await apiGet(`/match?limit=100&authoritative=true&_ts=${Date.now()}`, { cache: 'no-store' });
  if (!res.ok) throw new Error('Failed to fetch matches for verification.');
  const data = await res.json();
  const matches = data.matches || [];
  const match = matches.find((m) => m && m.match_id === matchId);
  if (!match) return null;
  return parseMatchLabel(match);
}

function labelHasPlayer(label, { userId, discordId }) {
  if (!label || typeof label !== 'object') return false;
  const players = Array.isArray(label.players) ? label.players : [];
  const u = userId ? String(userId) : '';
  const d = discordId ? String(discordId) : '';
  if (!u && !d) return false;

  return players.some((p) => {
    if (!p || typeof p !== 'object') return false;
    const pid = p.user_id || p.userId || p.nakama_id || p.nakamaId || p.id;
    if (u && pid && String(pid) === u) return true;
    const did = p.discord_id || p.discordId;
    if (d && did && String(did) === d) return true;
    return false;
  });
}

function kickSubjectForPlayer(player) {
  if (!player || typeof player !== 'object') return '';

  // The backend RPC `player/kick` expects a Nakama user_id (not a session_id).
  const userId = player.user_id || player.userId || player.nakama_id || player.nakamaId || player.id;
  if (userId) return String(userId);

  const evrId = player.evr_id || player.evrId || player.xp_id || player.xpId;
  if (evrId) return String(evrId);

  return '';
}

function enforcementDurationFromForm() {
  const value = Number(kickForm.value.suspension_value || 0);
  if (!Number.isFinite(value) || value <= 0) return '';

  switch (kickForm.value.suspension_unit) {
    case 'minutes':
      return `${value}m`;
    case 'hours':
      return `${value}h`;
    case 'days':
      return `${value}d`;
    case 'weeks':
      return `${value}w`;
    default:
      return '';
  }
}

function enforcementUserNotice() {
  // `enforcement/kick` requires a short notice shown to the user (<= 48 chars).
  const reason = String(kickForm.value.reason || '');
  const noticeMap = {
    hacks: 'Kicked: cheats/exploits',
    disrespect: 'Kicked: disrespectful behavior',
    prejudice: 'Kicked: prejudiced remarks',
    explicit: 'Kicked: explicit comments',
    discrimination: 'Kicked: discriminatory language',
    disruptive: 'Kicked: disruptive behavior',
    sensitive: 'Kicked: sensitive topics',
    harassment: 'Kicked: harassment/threats',
    exploits: 'Kicked: exploits',
    unsportsmanlike: 'Kicked: unsportsmanlike conduct',
    trolling: 'Kicked: trolling',
    violence: 'Kicked: illegal/violent content',
    callout: 'Kicked: public callouts',
    doxxing: 'Kicked: personal info sharing',
  };

  const notice = noticeMap[reason] || 'Kicked by moderator';
  return notice.length > 48 ? notice.slice(0, 48) : notice;
}

async function verifyKickEffect({ matchId, targetUserId, targetDiscordId }) {
  if (!matchId) return;

  try {
    const label = await fetchMatchLabelById(matchId);
    const stillThere = labelHasPlayer(label, { userId: targetUserId, discordId: targetDiscordId });

    if (stillThere) {
      kickStatusKind.value = 'warning';
      kickStatus.value =
        `${kickStatus.value}\n\n⚠️ Player still appears in the match label after kick.` +
        `\nThis usually means they reconnected immediately or the backend did not eject them from the authoritative match.` +
        `\nTry kick again, or use Shutdown as a last resort.`;
    }
  } catch (e) {
    // Don't fail the kick on verification errors.
    kickStatusKind.value = 'warning';
    kickStatus.value = `${kickStatus.value}\n\n⚠️ Kick verification failed: ${e?.message || 'unknown error'}`;
  }
}

async function submitKick() {
  kickStatus.value = '';
  kickStatusKind.value = 'success';

  if (!selectedPlayer.value) {
    kickStatusKind.value = 'error';
    kickStatus.value = 'No player selected.';
    return;
  }

  kickSubmitting.value = true;
  try {
    // The backend kick RPC expects a Nakama user_id. If the match label doesn't include it,
    // resolve it via account/lookup.
    let targetUserId = kickSubjectForPlayer(selectedPlayer.value);

    // Resolve user id only as a last resort.
    if (!targetUserId) {
      kickStatusKind.value = 'success';
      kickStatus.value = 'Resolving player identifier…';
      const resolvedUserId = await resolvePlayerUserId(selectedPlayer.value);
      if (!resolvedUserId) {
        throw new Error('Unable to resolve player identifier from match label (no session_id/user_id/discord_id/xp_id).');
      }
      targetUserId = String(resolvedUserId);
    }

    const matchId = selectedServer.value?.match_id;
    const targetDiscordId = selectedPlayer.value?.discord_id || selectedPlayer.value?.discordId;
    const labelUserId = selectedPlayer.value?.user_id || selectedPlayer.value?.userId;

    lastKickAttempt.value = {
      matchId,
      kickSubject: String(targetUserId),
      targetUserId: labelUserId ? String(labelUserId) : String(targetUserId),
      targetDiscordId: targetDiscordId ? String(targetDiscordId) : '',
    };

    // Prefer the newer enforcement RPC when doing suspension (and optionally when escalating),
    // but fall back to player/kick to preserve compatibility.
    let res;
    let data;

    if (kickForm.value.action === 'suspend') {
      const duration = enforcementDurationFromForm();
      if (!duration) throw new Error('Suspension duration is invalid.');

      res = await callRpc('enforcement/kick', {
        user_id: String(targetUserId),
        user_notice: enforcementUserNotice(),
        suspension_duration: duration,
        moderator_notes: String(kickForm.value.comments || ''),
        allow_private_lobbies: !!kickForm.value.allow_privates,
        require_community_values: !!kickForm.value.escalate,
      });
      data = await readResponsePayload(res);
      if (!res.ok) {
        throw new Error(`${formatRpcFailure('enforcement/kick', res, data)}\nuser_id: ${String(targetUserId)}`);
      }
    } else {
      // Regular kick.
      // First attempt enforcement/kick only if user toggled escalate.
      if (kickForm.value.escalate) {
        res = await callRpc('enforcement/kick', {
          user_id: String(targetUserId),
          user_notice: enforcementUserNotice(),
          moderator_notes: String(kickForm.value.comments || ''),
          allow_private_lobbies: !!kickForm.value.allow_privates,
          require_community_values: true,
        });
        data = await readResponsePayload(res);
      }

      if (!res || !res.ok) {
        res = await callRpc('player/kick', { user_id: String(targetUserId) });
        data = await readResponsePayload(res);
        if (!res.ok) {
          throw new Error(`${formatRpcFailure('player/kick', res, data)}\nuser_id: ${String(targetUserId)}`);
        }
      }
    }

    rememberKickAttempt(matchId, selectedPlayer.value);

    const kickedSessionIds = Array.isArray(data?.session_ids) ? data.session_ids : [];
    const sessionsKicked = typeof data?.sessions_kicked === 'number' ? data.sessions_kicked : null;
    const actionWord = kickForm.value.action === 'suspend' ? 'Enforcement sent' : 'Kick sent';

    if (kickedSessionIds.length) {
      kickStatus.value = `${actionWord}.\nDisconnected sessions: ${kickedSessionIds.join(', ')}\n\nRefreshing server state…`;
    } else if (sessionsKicked !== null) {
      kickStatus.value = `${actionWord}.\nSessions kicked: ${sessionsKicked}\n\nRefreshing server state…`;
    } else {
      kickStatus.value = `${actionWord}.\n\nRefreshing server state…`;
    }

    // Do not optimistically remove from UI.
    // Only the authoritative match label refresh should change player lists.

    // Refresh after short delays to let backend state settle (labels can lag).
    setTimeout(() => {
      fetchMyServers();
    }, 1200);
    setTimeout(() => {
      fetchMyServers();
    }, 2600);
    setTimeout(() => {
      fetchMyServers();
    }, 5200);
    setTimeout(() => {
      fetchMyServers();
    }, 8000);

    // Verify after refresh window whether player still appears.
    setTimeout(() => {
      verifyKickEffect({ matchId, targetUserId, targetDiscordId });
    }, 3000);
  } catch (e) {
    kickStatusKind.value = 'error';
    kickStatus.value = e?.message || 'Kick failed.';
  } finally {
    kickSubmitting.value = false;
  }
}

async function retryLastKick() {
  const attempt = lastKickAttempt.value;
  if (!attempt?.kickSubject) return;
  kickStatusKind.value = 'success';
  kickStatus.value = 'Retrying kick…';
  try {
    const res = await callRpc('player/kick', { user_id: String(attempt.kickSubject) });
    const data = await readResponsePayload(res);
    if (!res.ok) {
      throw new Error(`${formatRpcFailure('player/kick', res, data)}\nuser_id: ${String(attempt.kickSubject)}`);
    }

    rememberKickAttempt(attempt.matchId, {
      user_id: attempt.targetUserId || undefined,
      discord_id: attempt.targetDiscordId || undefined,
    });
    const kickedSessionIds = Array.isArray(data?.session_ids) ? data.session_ids : [];
    kickStatusKind.value = 'success';
    kickStatus.value = kickedSessionIds.length
      ? `Kick retried.\nDisconnected sessions: ${kickedSessionIds.join(', ')}`
      : 'Kick retried.';

    setTimeout(() => {
      fetchMyServers();
    }, 1200);
    setTimeout(() => {
      fetchMyServers();
    }, 2600);
    setTimeout(() => {
      fetchMyServers();
    }, 5200);
    setTimeout(() => {
      fetchMyServers();
    }, 8000);

    setTimeout(() => {
      verifyKickEffect({
        matchId: attempt.matchId,
        targetUserId: attempt.targetUserId,
        targetDiscordId: attempt.targetDiscordId,
      });
    }, 3000);
  } catch (e) {
    kickStatusKind.value = 'error';
    kickStatus.value = e?.message || 'Retry kick failed.';
  }
}

function openShutdown(server) {
  shutdownServer.value = server;
  shutdownStatus.value = '';
  shutdownStatusKind.value = 'success';
  shutdownModalOpen.value = true;

  // Reset form defaults.
  shutdownForm.value = {
    reason: '',
    custom_reason: '',
    use_grace: 'no',
    grace_value: 5,
    grace_unit: 'minutes',
  };
}

function openReport(server) {
  reportServer.value = server;
  reportStatus.value = '';
  reportStatusKind.value = 'success';
  reportModalOpen.value = true;
  reportForm.value = {
    issue_type: 'server_issue',
    severity: 'medium',
    details: '',
  };
}

function closeReport() {
  reportModalOpen.value = false;
  reportServer.value = null;
  reportSubmitting.value = false;
}

async function submitReport() {
  reportSubmitting.value = true;
  reportStatus.value = '';
  reportStatusKind.value = 'success';

  try {
    const server = reportServer.value;
    const payload = {
      issue_type: reportForm.value.issue_type,
      severity: reportForm.value.severity,
      details: reportForm.value.details,
      match_id: server?.match_id || null,
      guild_id: serverGuildId(server),
      reporter_user_id: userState.profile?.nakamaId || null,
      timestamp: new Date().toISOString(),
    };

    await navigator.clipboard.writeText(JSON.stringify(payload, null, 2));
    reportStatus.value = 'Report payload copied to clipboard.';
  } catch (e) {
    reportStatusKind.value = 'error';
    reportStatus.value = e?.message || 'Failed to copy report payload.';
  } finally {
    reportSubmitting.value = false;
  }
}

function closeShutdown() {
  shutdownModalOpen.value = false;
  shutdownServer.value = null;
  shutdownSubmitting.value = false;
}

function graceSecondsFromForm() {
  if (shutdownForm.value.use_grace !== 'yes') return 10;
  const value = Number(shutdownForm.value.grace_value || 0);
  if (!Number.isFinite(value) || value <= 0) return 10;

  const unit = shutdownForm.value.grace_unit;
  switch (unit) {
    case 'seconds':
      return value;
    case 'minutes':
      return value * 60;
    case 'hours':
      return value * 3600;
    default:
      return 10;
  }
}

async function submitShutdown() {
  shutdownStatus.value = '';
  shutdownStatusKind.value = 'success';

  if (!shutdownServer.value?.match_id) {
    shutdownStatusKind.value = 'error';
    shutdownStatus.value = 'Missing match_id.';
    return;
  }

  shutdownSubmitting.value = true;
  try {
    const grace_seconds = graceSecondsFromForm();
    const res = await callRpc('match/terminate', {
      match_id: shutdownServer.value.match_id,
      grace_seconds,
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.message || 'Shutdown failed.');
    shutdownStatus.value = 'Shutdown signal sent.';

    await fetchMyServers();
  } catch (e) {
    shutdownStatusKind.value = 'error';
    shutdownStatus.value = e?.message || 'Shutdown failed.';
  } finally {
    shutdownSubmitting.value = false;
  }
}

function formatMode(mode) {
  if (!mode) return 'UNKNOWN';
  return String(mode).toUpperCase();
}

function normalizeLobbyType(label) {
  if (!label || typeof label !== 'object') return '';

  const raw = label.lobby_type ?? label.lobbyType ?? label.lobbytype ?? null;
  if (typeof raw === 'string') {
    const s = raw.trim().toLowerCase();
    if (s === 'public' || s === 'private' || s === 'unassigned') return s;
    if (s === 'unk' || s === 'unknown') return '';
  }
  if (typeof raw === 'number' && Number.isFinite(raw)) {
    if (raw === 0) return 'public';
    if (raw === 1) return 'private';
    if (raw === 2) return 'unassigned';
  }

  // Fallback: infer from mode if lobby_type is missing.
  const mode = String(label.mode || '').toLowerCase();
  if (mode.includes('private') || mode.includes('_private')) return 'private';
  if (mode) return 'public';
  return '';
}

function lobbyTypeBadge(label) {
  const t = normalizeLobbyType(label);
  if (t === 'public') return 'PUBLIC';
  if (t === 'private') return 'PRIVATE';
  if (t === 'unassigned') return 'UNASSIGNED';
  return '';
}

function lobbyTypeBadgeClass(label) {
  const t = normalizeLobbyType(label);
  if (t === 'public') return 'bg-sky-900 text-sky-200 border border-sky-700/30';
  if (t === 'private') return 'bg-purple-900 text-purple-200 border border-purple-700/30';
  if (t === 'unassigned') return 'bg-slate-700 text-slate-200 border border-slate-600/40';
  return 'bg-slate-700 text-slate-200 border border-slate-600/40';
}

function getModeClass(mode) {
  const m = String(mode || '').toLowerCase();
  if (m.includes('private')) return 'bg-purple-900 text-purple-200';
  if (m.includes('public')) return 'bg-sky-900 text-sky-200';
  return 'bg-slate-700 text-slate-200';
}

function rawTeamValue(player) {
  if (!player || typeof player !== 'object') return null;
  return player.team ?? player.team_id ?? player.teamId ?? player.role ?? null;
}

function hasAnyPlayers(players) {
  return Array.isArray(players) && players.length > 0;
}

function playerKey(player, idx) {
  if (!player || typeof player !== 'object') return `p:${idx}`;
  return player.user_id || player.session_id || player.evr_id || player.display_name || player.username || `p:${idx}`;
}

function groupedPlayers(players) {
  const list = Array.isArray(players) ? players : [];

  const groups = {
    blue: [],
    orange: [],
    spectator: [],
    moderator: [],
    social: [],
    unk: [],
  };

  for (const p of list) {
    const team = normalizeTeam(p);
    if (groups[team]) groups[team].push(p);
    else groups.unk.push(p);
  }

  const countReal = (arr) => arr.filter((p) => !p?.__portalGhost).length;

  return [
    { key: 'blue', title: 'Blue Team', players: groups.blue, count: countReal(groups.blue) },
    { key: 'orange', title: 'Orange Team', players: groups.orange, count: countReal(groups.orange) },
    { key: 'spectator', title: 'Spectators', players: groups.spectator, count: countReal(groups.spectator) },
    { key: 'moderator', title: 'Moderators', players: groups.moderator, count: countReal(groups.moderator) },
    { key: 'social', title: 'Social', players: groups.social, count: countReal(groups.social) },
    { key: 'unk', title: 'Other', players: groups.unk, count: countReal(groups.unk) },
  ];
}

function normalizeTeam(player) {
  const t = rawTeamValue(player);

  // Server-side MatchLabel marshals TeamIndex to strings like "blue"/"orange".
  if (typeof t === 'string') return t.toLowerCase();

  // Fallback if any client/older label uses integers.
  if (typeof t === 'number') {
    switch (t) {
      case 0:
        return 'blue';
      case 1:
        return 'orange';
      case 2:
        return 'spectator';
      case 3:
        return 'social';
      case 4:
        return 'moderator';
      default:
        return 'unk';
    }
  }

  return 'unk';
}

function teamLabel(player) {
  const team = normalizeTeam(player);
  switch (team) {
    case 'blue':
      return 'BLUE';
    case 'orange':
      return 'ORANGE';
    case 'spectator':
      return 'SPEC';
    case 'moderator':
      return 'MOD';
    case 'social':
      return 'SOCIAL';
    default:
      return 'UNK';
  }
}

function teamBadgeClass(player) {
  const team = normalizeTeam(player);
  switch (team) {
    case 'blue':
      return 'bg-sky-900 text-sky-200 border border-sky-700/30';
    case 'orange':
      return 'bg-orange-900 text-orange-200 border border-orange-700/30';
    case 'spectator':
      return 'bg-slate-700 text-slate-200 border border-slate-600/40';
    case 'moderator':
      return 'bg-purple-900 text-purple-200 border border-purple-700/30';
    case 'social':
      return 'bg-emerald-900 text-emerald-200 border border-emerald-700/30';
    default:
      return 'bg-slate-700 text-slate-200 border border-slate-600/40';
  }
}

function formatPing(player) {
  if (!player || typeof player !== 'object') return '';
  const ping = player.ping_ms ?? player.pingMillis ?? player.ping ?? player.latency_ms ?? null;
  if (typeof ping !== 'number' || !Number.isFinite(ping) || ping <= 0) return '';
  return `${Math.round(ping)}ms`;
}

function discordUsernameForPlayer(player) {
  if (!player || typeof player !== 'object') return '';
  // In this fork, PlayerInfo.Username is typically the Discord username (Nakama username from Discord auth).
  const v =
    player.discord_username ??
    player.discordUsername ??
    player.discord_name ??
    player.discordName ??
    player.username ??
    '';
  return typeof v === 'string' ? v.trim() : String(v || '').trim();
}

function displayNameForPlayer(player) {
  if (!player || typeof player !== 'object') return '';
  const v = player.display_name ?? player.displayName ?? '';
  return typeof v === 'string' ? v.trim() : String(v || '').trim();
}

function formatPlayerName(player) {
  const display = displayNameForPlayer(player);
  const discord = discordUsernameForPlayer(player);
  if (display && discord && display.toLowerCase() !== discord.toLowerCase()) return `${display} (${discord})`;
  return display || discord || '?';
}

function playerPrimaryName(player) {
  const display = displayNameForPlayer(player);
  const discord = discordUsernameForPlayer(player);
  return display || discord || '?';
}

function playerSecondaryName(player) {
  const display = displayNameForPlayer(player);
  const discord = discordUsernameForPlayer(player);
  // Only show secondary when we have a display name to anchor it to.
  // If display and discord are the same, still show the username (per UX request).
  if (!display || !discord) return '';
  return discord;
}

function serverLocation(server) {
  const label = server?.label;
  const parts = [];
  if (label?.server_city || label?.broadcaster?.city) parts.push(label.server_city || label.broadcaster.city);
  if (label?.server_country || label?.broadcaster?.country_code) parts.push(label.server_country || label.broadcaster.country_code);
  if (label?.server_region || label?.broadcaster?.default_region) parts.push(label.server_region || label.broadcaster.default_region);
  return parts.filter(Boolean).join(', ');
}

function formatEndpoint(endpoint) {
  if (!endpoint) return '';
  // endpoint is often "ip:port"; keep as-is but trim spaces.
  return String(endpoint).trim();
}

function serverPlayerCount(server) {
  const label = server?.label;
  const fromCount = Number(label?.player_count);
  if (Number.isFinite(fromCount) && fromCount >= 0) return fromCount;
  const players = Array.isArray(label?.players) ? label.players : [];
  return players.length;
}

function taxiUrlForServer(server) {
  const matchId = server?.match_id;
  if (!matchId) return null;

  // Echo Taxi format: https://echo.taxi/spark://c/<match_id>
  return `https://echo.taxi/spark://c/${encodeURIComponent(String(matchId))}`;
}

function shouldShowTaxi(server) {
  if (!server?.match_id) return false;
  return serverPlayerCount(server) > 0;
}

async function handleTaxiCopy(server) {
  const taxiUrl = taxiUrlForServer(server);
  if (!taxiUrl) return;

  try {
    await navigator.clipboard.writeText(String(taxiUrl));
  } catch (_) {
    // Non-fatal.
  }
}

async function handleTaxiJoin(server) {
  const taxiUrl = taxiUrlForServer(server);
  if (!taxiUrl) return;

  try {
    await navigator.clipboard.writeText(String(taxiUrl));
  } catch (_) {
    // Non-fatal.
  }

  try {
    window.open(String(taxiUrl), '_blank', 'noopener,noreferrer');
  } catch (_) {
    // Ignore popup blockers.
  }
}

onMounted(() => {
  fetchMyServers();
});
</script>
