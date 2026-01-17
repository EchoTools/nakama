<template>
  <div class="max-w-7xl mx-auto mt-10 px-10 text-gray-100">
    <div class="flex items-center justify-between mb-6">
      <div class="flex items-center gap-4">
        <span class="text-3xl leading-none">🔍</span>
        <h2 class="text-3xl font-semibold">Player Lookup</h2>
      </div>
      <button
        type="button"
        class="inline-flex items-center gap-2 px-3 py-1.5 rounded bg-slate-700 hover:bg-slate-600 text-slate-100 border border-slate-500/40 text-sm font-semibold transition disabled:opacity-60 disabled:cursor-not-allowed"
        :disabled="loading"
        @click="refreshPlayerTab"
      >
        🔄 Refresh
      </button>
    </div>

    <div class="mb-6">
      <div class="relative">
        <input
          id="player-search"
          v-model="userId"
          type="text"
          placeholder="Enter player name / Username / EvrID / NakamaID / DiscordID"
          class="w-full px-4 py-3 rounded bg-slate-900/60 text-slate-100 placeholder-slate-400 border border-slate-700/60 focus:outline-none focus:ring-2 focus:ring-sky-500"
          autocomplete="off"
          @input="onSearchInput"
          @focus="onSearchInput"
          @blur="hideAutocompleteDelayed"
          @keydown.down.prevent="navigateAutocomplete(1)"
          @keydown.up.prevent="navigateAutocomplete(-1)"
          @keydown.escape.prevent="closeAutocomplete"
          @keydown.enter.prevent="handleSearchEnter"
        />

        <div
          v-if="showAutocomplete && autocompleteResults.length"
          class="absolute z-20 mt-2 w-full rounded-lg border border-slate-700/60 bg-slate-900/95 shadow-lg shadow-black/40 overflow-hidden"
        >
          <button
            v-for="(opt, idx) in autocompleteResults"
            :key="opt.key"
            type="button"
            class="w-full px-4 py-2 text-left text-sm hover:bg-slate-800/70 transition flex items-center justify-between gap-3"
            :class="idx === selectedAutocompleteIndex ? 'bg-slate-800/80' : ''"
            @mousedown.prevent="selectResult(opt)"
          >
            <div class="min-w-0">
              <div class="text-slate-100 truncate">
                {{ opt.primary }}
                <span v-if="opt.secondary" class="ml-1 text-xs text-slate-400">({{ opt.secondary }})</span>
              </div>
              <div v-if="opt.hint" class="text-[11px] text-slate-500 truncate">{{ opt.hint }}</div>
            </div>
            <div v-if="opt.badge" class="shrink-0 px-2 py-0.5 rounded text-[10px] font-semibold border"
              :class="opt.badgeClass"
            >
              {{ opt.badge }}
            </div>
          </button>
        </div>
      </div>
    </div>
    <div v-if="!hasResults && !loading && !error" class="text-gray-100/80 mb-6">Start typing to search...</div>

    <!-- Online Players (derived from active matches) -->
    <div
      v-if="!hasResults && !loading"
      class="bg-slate-800 border border-slate-600/50 rounded-xl p-5 shadow-sm shadow-black/30 mb-8"
    >
      <div class="flex items-center justify-between gap-3 mb-3">
        <h3 class="text-xl font-semibold">Online Players</h3>
        <input
          v-model="onlinePlayersFilter"
          type="text"
          placeholder="Filter…"
          class="w-56 px-3 py-1.5 rounded bg-slate-900/60 text-slate-100 placeholder-slate-500 border border-slate-700/60 focus:outline-none focus:ring-2 focus:ring-sky-500 text-sm"
        />
        <button
          type="button"
          class="px-3 py-1.5 rounded bg-slate-700 hover:bg-slate-600 border border-slate-600/50 text-slate-100 text-sm font-semibold disabled:opacity-60 disabled:cursor-not-allowed"
          :disabled="loadingOnlinePlayers"
          @click="fetchOnlinePlayers"
        >
          🔄 Refresh
        </button>
      </div>

      <div class="text-xs text-slate-400 mb-4">
        This list is derived from current match labels (players currently in active authoritative matches).
      </div>

      <div v-if="onlinePlayersError" class="text-red-400 text-sm mb-3">{{ onlinePlayersError }}</div>
      <div v-else-if="loadingOnlinePlayers" class="text-gray-400 text-sm">Loading online players…</div>
      <div v-else-if="onlinePartyGroups.length === 0" class="text-gray-400 text-sm">No online players found.</div>
      <div v-else class="space-y-6">
        <div v-for="grp in onlinePartyGroups" :key="grp.key" class="space-y-2">
          <div class="flex items-center justify-between">
            <div class="text-xs font-semibold text-gray-300 tracking-wide">
              {{ grp.title }}
              <span class="text-gray-500 font-normal">({{ grp.players.length }})</span>
            </div>
            <span v-if="grp.partyId" class="text-[11px] text-gray-500 font-mono">{{ grp.partyId }}</span>
          </div>

          <div class="space-y-2">
            <button
              v-for="(p, idx) in grp.players"
              :key="(p.user_id && String(p.user_id).toLowerCase() !== ZERO_UUID ? p.user_id : '') || p.discord_id || p.evr_id || `p:${grp.key}:${idx}`"
              type="button"
              class="w-full flex items-center justify-between gap-3 px-3 py-2 rounded bg-slate-900/70 hover:bg-slate-700/60 border border-slate-700/60 text-sm transition text-left"
              @click="lookupOnlinePlayer(p)"
            >
              <div class="flex items-center gap-3 min-w-0">
                <div
                  class="h-9 w-9 rounded-full bg-slate-900/60 border border-slate-700/60 overflow-hidden flex items-center justify-center shrink-0"
                  :title="onlineAvatarTitleForPlayer(p)"
                >
                  <img
                    v-if="onlineAvatarUrlForPlayer(p)"
                    :src="onlineAvatarUrlForPlayer(p)"
                    :alt="onlineAvatarAltForPlayer(p)"
                    class="h-full w-full object-cover"
                    loading="lazy"
                    referrerpolicy="no-referrer"
                    @error="markOnlineAvatarError(p)"
                  />
                  <span v-else class="text-slate-200 font-semibold text-sm select-none">{{ onlineAvatarFallbackForPlayer(p) }}</span>
                </div>

                <div class="min-w-0">
                  <div class="text-slate-100 truncate">
                    {{ displayNameForPlayer(p) || discordUsernameForPlayer(p) || '?' }}
                  </div>
                  <div v-if="discordUsernameForPlayer(p)" class="text-[11px] text-slate-400 font-mono truncate leading-tight">
                    {{ discordUsernameForPlayer(p) }}
                  </div>
                  <div class="text-[11px] text-slate-500 font-mono truncate">
                    {{ (p.user_id || p.discord_id || p.evr_id || '').toString() }}
                  </div>
                </div>
              </div>
              <div class="shrink-0 text-[11px] text-slate-400">
                {{ formatMode(p._mode) || '' }}
              </div>
            </button>
          </div>
        </div>
      </div>
    </div>

    <div v-if="error" class="text-red-400 mb-4">{{ error }}</div>

    <!-- Results -->
    <div v-if="hasResults" class="space-y-6">
      <!-- Section Jump / Filter (only when an account is loaded) -->
      <div class="bg-slate-900/40 border border-slate-700/60 rounded-xl p-4 shadow-sm shadow-black/30">
        <div class="flex items-center justify-between gap-3">
          <div class="text-sm text-slate-300 font-semibold">Jump to section</div>
          <div class="flex items-center gap-2">
            <select
              v-model="selectedSection"
              class="rounded bg-slate-900/60 border border-slate-700/60 px-3 py-2 text-sm text-slate-100"
              @change="handleSectionChange"
            >
              <option value="all">All</option>
              <option value="account">EchoVRCE Account</option>
              <option value="alternates" :disabled="!canViewLoginHistory || !alternateAccounts.length">Suspected Alternate Accounts</option>
              <option value="enforcement" :disabled="!canViewEnforcement || !suspensionsByGroup.length">Enforcement History</option>
              <option value="pastNames" :disabled="!pastDisplayNamesByGuild.length">Past Display Names</option>
              <option value="servers" :disabled="!(gameServers.length || ownedServers.length)">Active Game / Owned Servers</option>
            </select>
          </div>
        </div>
      </div>

      <!-- Account Card -->
      <div
        ref="sectionAccount"
        v-show="sectionVisible('account')"
        class="bg-slate-800 border border-slate-600/50 rounded-xl p-5 shadow-sm shadow-black/30"
      >
        <div class="flex items-center justify-between">
          <div class="flex items-center gap-4 min-w-0">
            <div
              class="h-14 w-14 rounded-full bg-slate-900/60 border border-slate-700/60 overflow-hidden flex items-center justify-center shrink-0"
              :title="playerAvatarTitle"
            >
              <img
                v-if="playerAvatarUrl"
                :src="playerAvatarUrl"
                :alt="playerAvatarAlt"
                class="h-full w-full object-cover"
                loading="lazy"
                referrerpolicy="no-referrer"
                @error="avatarLoadError = true"
              />
              <span v-else class="text-slate-200 font-semibold text-lg select-none">{{ playerAvatarFallback }}</span>
            </div>

            <div v-if="debugAvatars" class="text-xs text-slate-400 break-all max-w-[520px]">
              <div>raw: {{ user?.avatar_url || user?.avatarUrl || user?.avatarURL || user?.metadata?.avatar_url || user?.metadata?.avatarUrl || '—' }}</div>
              <div>discord/custom id: {{ discordIdFromObj(user) || '—' }}</div>
              <div>candidate: {{ playerAvatarUrlCandidate || '—' }}</div>
              <div>effective: {{ playerAvatarUrl || '—' }} (error={{ avatarLoadError }})</div>
            </div>

            <div class="min-w-0">
              <h3 class="text-xl font-semibold">EchoVRCE Account</h3>
              <p class="text-sm text-gray-400">
                Nakama ID:
                <span class="font-mono text-xs text-gray-300">{{ user?.id }}</span>
              </p>
            </div>
          </div>
        </div>
        <div class="grid grid-cols-1 md:grid-cols-3 gap-4 mt-4">
          <div>
            <div class="text-sm text-gray-400">Username</div>
            <div class="text-base">{{ user?.username || '—' }}</div>
          </div>
          <div>
            <div class="text-sm text-gray-400">Party</div>
            <div class="text-base">
              <span v-if="currentPartyId" class="font-mono">{{ currentPartyId }}</span>
              <span v-else>—</span>
              <span v-if="!currentPartyId" class="text-xs text-gray-500 ml-2">(only shown while in-game)</span>
            </div>
          </div>
          <div>
            <div class="text-sm text-gray-400">Discord ID</div>
            <div class="text-base font-mono">{{ discordIdText }}</div>
          </div>
          <div>
            <div class="text-sm text-gray-400">Created</div>
            <div class="text-base">{{ createdAtText }}</div>
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
            v-else-if="recentLoginsList.length"
            class="text-sm bg-slate-900/40 rounded-lg p-3 border border-slate-700/60"
          >
            <ul class="space-y-2">
              <li v-for="row in recentLoginsList" :key="row.xpid" class="flex items-center justify-between gap-3">
                <span class="text-gray-300">{{ row.when }}</span>
                <span class="text-gray-200 bg-slate-900/60 border border-slate-700/60 rounded px-2 py-0.5 text-xs">
                  {{ row.xpid }}
                </span>
              </li>
            </ul>
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
      <div
        v-if="canViewLoginHistory && alternateAccounts.length"
        ref="sectionAlternates"
        v-show="sectionVisible('alternates')"
        class="bg-slate-800 border border-slate-600/50 rounded-xl p-5 shadow-sm shadow-black/30"
      >
        <div class="flex items-center justify-between gap-3 mb-2">
          <h3 class="text-xl font-semibold">Suspected Alternate Accounts</h3>
          <input
            v-model="alternateAccountsFilter"
            type="text"
            placeholder="Filter…"
            class="w-64 px-3 py-1.5 rounded bg-slate-900/60 text-slate-100 placeholder-slate-500 border border-slate-700/60 focus:outline-none focus:ring-2 focus:ring-sky-500 text-sm"
          />
        </div>
        <div class="space-y-4">
          <div
            v-for="alt in filteredAlternateAccounts"
            :key="alt.userId"
            class="border border-slate-700/60 rounded-lg p-3 bg-slate-900/40"
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
            <div class="text-xs font-semibold text-gray-400 mb-2">Matches</div>
            <div class="flex flex-wrap gap-2 mb-3">
              <span
                v-for="item in alt.items"
                :key="item"
                class="px-2 py-0.5 text-xs rounded bg-slate-900/70 border border-slate-700/60 text-gray-200"
              >
                {{ item }}
              </span>
            </div>

            <!-- Enforcement History for this alternate -->
            <div
              v-if="canViewEnforcement && alt.enforcement && getAltSuspensions(alt.enforcement).length"
              class="mt-3 pt-3 border-t border-slate-700/60"
            >
              <div class="text-xs font-semibold text-gray-400 mb-2">Enforcement History:</div>
              <div class="space-y-3">
                <div
                  v-for="grp in getAltSuspensions(alt.enforcement)"
                  :key="grp.groupId"
                  class="bg-slate-900/40 border border-slate-700/60 rounded-lg px-3 py-2"
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
      <div
        v-if="canViewEnforcement && suspensionsByGroup.length"
        ref="sectionEnforcement"
        v-show="sectionVisible('enforcement')"
        class="bg-slate-800 border border-slate-600/50 rounded-xl p-5 shadow-sm shadow-black/30"
      >
        <div class="flex items-center justify-between gap-3 mb-4">
          <h3 class="text-xl font-semibold">Enforcement History</h3>
          <input
            v-model="enforcementFilter"
            type="text"
            placeholder="Filter…"
            class="w-64 px-3 py-1.5 rounded bg-slate-900/60 text-slate-100 placeholder-slate-500 border border-slate-700/60 focus:outline-none focus:ring-2 focus:ring-sky-500 text-sm"
          />
        </div>
        <div class="space-y-4">
          <div
            v-for="grp in filteredSuspensionsByGroup"
            :key="grp.groupId"
            class="border border-slate-700/60 rounded-xl bg-slate-900/40 overflow-hidden"
          >
            <div class="bg-slate-800 px-4 py-2 border-b border-slate-600/50">
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
                  <div class="text-base text-gray-200 bg-slate-950/30 rounded px-3 py-2 border border-slate-700/60">
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
                <div v-if="rec.isVoid" class="mt-3 pt-3 border-t border-slate-700/60">
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
      <div
        v-if="pastDisplayNamesByGuild.length"
        ref="sectionPastNames"
        v-show="sectionVisible('pastNames')"
        class="bg-slate-800 border border-slate-600/50 rounded-xl p-5 shadow-sm shadow-black/30"
      >
        <div class="flex items-center justify-between gap-3 mb-4">
          <h3 class="text-xl font-semibold">Past Display Names</h3>
          <input
            v-model="pastDisplayNamesFilter"
            type="text"
            placeholder="Filter…"
            class="w-56 px-3 py-1.5 rounded bg-slate-900/60 text-slate-100 placeholder-slate-500 border border-slate-700/60 focus:outline-none focus:ring-2 focus:ring-sky-500 text-sm"
          />
        </div>
        <div class="space-y-4">
          <div
            v-for="group in filteredPastDisplayNamesByGuild"
            :key="group.groupId"
            class="border border-slate-700/60 rounded-xl bg-slate-900/40 overflow-hidden"
          >
            <div class="bg-slate-800 px-4 py-2 border-b border-slate-600/50">
              <div class="font-semibold">{{ groupName(group.groupId) }}</div>
            </div>
            <div class="p-3 space-y-2">
              <div
                v-for="entry in group.names"
                :key="entry.name"
                class="flex items-center justify-between gap-2 text-sm"
              >
                <div class="flex items-center gap-2">
                  <span class="text-gray-200 bg-slate-900/70 border border-slate-700/60 rounded px-2 py-0.5 text-xs">
                    {{ entry.name }}
                  </span>
                  <button
                    @click="copyToClipboard(entry.name)"
                    type="button"
                    class="px-2 py-0.5 text-xs rounded bg-slate-700 hover:bg-slate-600 border border-slate-600/50 text-slate-100"
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

      <!-- Active + Owned Servers Card -->
      <div
        v-if="gameServers.length || ownedServers.length"
        ref="sectionServers"
        v-show="sectionVisible('servers')"
        class="bg-slate-800 border border-slate-600/50 rounded-xl p-5 shadow-sm shadow-black/30"
      >
        <div class="flex items-center justify-between gap-3 mb-4">
          <h3 class="text-xl font-semibold">Active Game / Owned Servers</h3>
          <input
            v-model="activeServersFilter"
            type="text"
            placeholder="Filter…"
            class="w-56 px-3 py-1.5 rounded bg-slate-900/60 text-slate-100 placeholder-slate-500 border border-slate-700/60 focus:outline-none focus:ring-2 focus:ring-sky-500 text-sm"
          />
        </div>

        <div class="space-y-6">
          <div v-if="filteredGameServers.length" class="space-y-3">
            <div class="text-xs font-semibold text-gray-300 tracking-wide">
              Current Game
              <span class="text-gray-500 font-normal">({{ filteredGameServers.length }})</span>
            </div>

            <div class="space-y-3">
              <div
                v-for="server in filteredGameServers"
                :key="`active:${server.match_id}`"
                class="border border-slate-700/60 rounded-xl bg-slate-900/40 p-4"
              >
                <!-- Server Header -->
                <div class="flex items-center justify-between mb-3">
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
                  </div>
                  <div class="flex items-center gap-2">
                    <button
                      v-if="shouldShowTaxi(server)"
                      type="button"
                      class="px-2.5 py-1 rounded bg-sky-700/70 hover:bg-sky-600 border border-slate-600/50 text-slate-100 text-xs font-semibold"
                      :title="taxiButtonTitle()"
                      @click="handleTaxi(server)"
                    >
                      🚕 Taxi Link
                    </button>
                    <span class="text-xs text-gray-500 font-mono">{{ server.match_id?.slice(0, 8) }}…</span>
                  </div>
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
                <div v-if="canViewServerIPs && server.label?.broadcaster?.endpoint" class="mt-3 pt-3 border-t border-slate-700/60">
                  <div class="text-xs text-gray-400 mb-1">Endpoint</div>
                  <div class="font-mono text-xs text-gray-300 bg-slate-900/70 rounded px-2 py-1 border border-slate-700/60">
                    {{ formatEndpoint(server.label?.broadcaster?.endpoint) }}
                  </div>
                </div>

                <!-- Players List (if available) -->
                <div v-if="server.label?.players?.length" class="mt-3 pt-3 border-t border-slate-700/60">
                  <div class="text-xs text-gray-400 mb-2">Players</div>
                  <div class="flex flex-wrap gap-2">
                    <span
                      v-for="player in server.label.players"
                      :key="player.user_id || player.display_name"
                      class="px-2 py-0.5 text-xs rounded bg-slate-900/70 border border-slate-700/60"
                    >
                      <span class="inline-flex flex-col leading-tight">
                        <span>{{ displayNameForPlayer(player) || discordUsernameForPlayer(player) || '?' }}</span>
                        <span
                          v-if="displayNameForPlayer(player) && discordUsernameForPlayer(player)"
                          class="text-[10px] text-slate-400 font-mono"
                        >
                          {{ discordUsernameForPlayer(player) }}
                        </span>
                      </span>
                    </span>
                  </div>
                </div>

                <!-- Match Timestamps -->
                <div class="mt-3 pt-3 border-t border-slate-700/60 flex items-center gap-4 text-xs text-gray-500">
                  <span v-if="server.label?.created_at">Created: {{ formatRelative(server.label.created_at) }}</span>
                  <span v-if="server.label?.start_time">Started: {{ formatRelative(server.label.start_time) }}</span>
                </div>
              </div>
            </div>
          </div>

          <div v-if="filteredOwnedServers.length" class="space-y-3">
            <div class="text-xs font-semibold text-gray-300 tracking-wide">
              Owned Servers
              <span class="text-gray-500 font-normal">({{ filteredOwnedServers.length }})</span>
            </div>

            <div class="space-y-3">
              <div
                v-for="server in filteredOwnedServers"
                :key="`owned:${server.match_id}`"
                class="border border-slate-700/60 rounded-xl bg-slate-900/40 p-4"
              >
                <!-- Server Header -->
                <div class="flex items-center justify-between mb-3">
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
                  </div>
                  <div class="flex items-center gap-2">
                    <button
                      v-if="shouldShowTaxi(server)"
                      type="button"
                      class="px-2.5 py-1 rounded bg-sky-700/70 hover:bg-sky-600 border border-slate-600/50 text-slate-100 text-xs font-semibold"
                      :title="taxiButtonTitle()"
                      @click="handleTaxi(server)"
                    >
                      🚕 Taxi Link
                    </button>
                    <span class="text-xs text-gray-500 font-mono">{{ server.match_id?.slice(0, 8) }}…</span>
                  </div>
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
                <div v-if="canViewServerIPs && server.label?.broadcaster?.endpoint" class="mt-3 pt-3 border-t border-slate-700/60">
                  <div class="text-xs text-gray-400 mb-1">Endpoint</div>
                  <div class="font-mono text-xs text-gray-300 bg-slate-900/70 rounded px-2 py-1 border border-slate-700/60">
                    {{ formatEndpoint(server.label?.broadcaster?.endpoint) }}
                  </div>
                </div>

                <!-- Players List (if available) -->
                <div v-if="server.label?.players?.length" class="mt-3 pt-3 border-t border-slate-700/60">
                  <div class="text-xs text-gray-400 mb-2">Players</div>
                  <div class="flex flex-wrap gap-2">
                    <span
                      v-for="player in server.label.players"
                      :key="player.user_id || player.display_name"
                      class="px-2 py-0.5 text-xs rounded bg-slate-900/70 border border-slate-700/60"
                    >
                      <span class="inline-flex flex-col leading-tight">
                        <span>{{ displayNameForPlayer(player) || discordUsernameForPlayer(player) || '?' }}</span>
                        <span
                          v-if="displayNameForPlayer(player) && discordUsernameForPlayer(player)"
                          class="text-[10px] text-slate-400 font-mono"
                        >
                          {{ discordUsernameForPlayer(player) }}
                        </span>
                      </span>
                    </span>
                  </div>
                </div>

                <!-- Match Timestamps -->
                <div class="mt-3 pt-3 border-t border-slate-700/60 flex items-center gap-4 text-xs text-gray-500">
                  <span v-if="server.label?.created_at">Created: {{ formatRelative(server.label.created_at) }}</span>
                  <span v-if="server.label?.start_time">Started: {{ formatRelative(server.label.start_time) }}</span>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
      <div v-else-if="loadingServers" class="bg-slate-800 border border-slate-600/50 rounded-xl p-5 shadow-sm shadow-black/30">
        <h3 class="text-xl font-semibold mb-2">Active Game / Owned Servers</h3>
        <div class="text-gray-400">Loading active game and owned servers...</div>
      </div>
      <div v-else-if="user" class="bg-slate-800 border border-slate-600/50 rounded-xl p-5 shadow-sm shadow-black/30">
        <h3 class="text-xl font-semibold mb-2">Active Game / Owned Servers</h3>
        <div class="text-gray-400">No active game or owned servers found for this player.</div>
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
const gameServers = ref([]); // Matches where this player is currently listed in the match label
const ownedServers = ref([]); // Matches where this player is the operator/owner
const loadingServers = ref(false);
const resolvedDiscordId = ref('');
let autocompleteTimeout = null;

const onlinePlayers = ref([]);
const loadingOnlinePlayers = ref(false);
const onlinePlayersError = ref('');
const ZERO_UUID = '00000000-0000-0000-0000-000000000000';

// Avatar
const avatarLoadError = ref(false);

// Online players avatar cache (by Nakama user_id)
const onlineAvatarsByUserId = ref(new Map());
const onlineAvatarErrorUserIds = ref(new Set());
const onlineDiscordIdsByUserId = ref(new Map());

// Per-card local filters
const onlinePlayersFilter = ref('');
const activeServersFilter = ref('');
const pastDisplayNamesFilter = ref('');
const enforcementFilter = ref('');
const alternateAccountsFilter = ref('');

// Results section dropdown (only when an account is loaded)
const selectedSection = ref('all');
const sectionAccount = ref(null);
const sectionAlternates = ref(null);
const sectionEnforcement = ref(null);
const sectionPastNames = ref(null);
const sectionServers = ref(null);

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
  } else {
    // No specific player selected: show online list.
    fetchOnlinePlayers();
  }
});

function refreshPlayerTab() {
  // Refresh should re-load the current player (if any) rather than clearing.
  // If no player is loaded yet, fall back to looking up whatever is in the input.
  if (loading.value) return;

  const routeIdentifier =
    route.params.identifier && route.params.identifier !== ':identifier'
      ? decodeURIComponent(route.params.identifier)
      : '';
  const currentUserId = user.value?.id || routeIdentifier;

  error.value = '';

  if (currentUserId) {
    refreshPlayerById(currentUserId);
    return;
  }

  if (userId.value.trim()) {
    lookupPlayer();
    return;
  }
}

async function refreshPlayerById(uid) {
  const prevTitle = document.title;
  loading.value = true;
  try {
    // Build storage object IDs based on permissions
    const storageObjectIds = [{ collection: 'DisplayName', key: 'history', userId: uid }];

    if (canViewLoginHistory.value) {
      storageObjectIds.push({ collection: 'Login', key: 'history', userId: uid });
    }

    if (canViewEnforcement.value) {
      storageObjectIds.push({ collection: 'Enforcement', key: 'journal', userId: uid });
    }

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

    // Update core refs without wiping existing view first.
    user.value = u;
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
        // Keep existing guildGroups on failure; show error.
        error.value = e?.message || 'Failed to fetch guild groups.';
      }
    }

    const objects = (storageJson && storageJson.objects) || [];
    const byKey = new Map();
    for (const obj of objects) {
      byKey.set(`${obj.collection}/${obj.key}`, obj);
    }
    loginHistory.value = parseStorageValue(byKey.get('Login/history'));
    journal.value = parseStorageValue(byKey.get('Enforcement/journal'));
    displayNameHistory.value = parseStorageValue(byKey.get('DisplayName/history'));

    // Recompute derived/auxiliary data.
    await resolveAlternateUsernames();
    await resolveEnforcerUsernames();
    await fetchAlternateEnforcement();
    await fetchGameServers(uid);
  } catch (e) {
    error.value = e?.message || 'Refresh failed.';
    document.title = prevTitle;
  } finally {
    loading.value = false;
  }
}

// Restore original title on unmount
onUnmounted(() => {
  document.title = originalTitle;
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

      selectedSection.value = 'all';

      // Back to "no player selected" state: refresh online list.
      fetchOnlinePlayers();
    }
  }
);

watch(
  () => user.value?.id,
  () => {
    // Reset avatar error state whenever the loaded user changes.
    avatarLoadError.value = false;
  }
);

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

  // Protocol-relative URLs: treat as https.
  if (s.startsWith('//')) return `https:${s}`;

  // Host-only forms (rare, but seen in some stored values).
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
  // Discord avatar hashes look like hex-ish strings, sometimes with "a_" prefix for animated.
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

function avatarUrlFromUser(u, fallbackDiscordId) {
  if (!u || typeof u !== 'object') return '';
  const m = metadataObjectFromAny(u);
  const raw =
    u.avatar_url ??
    u.avatarUrl ??
    u.avatarURL ??
    m?.avatar_url ??
    m?.avatarUrl ??
    m?.avatarURL ??
    '';
  const s = normalizeAvatarUrlCandidate(raw);

  // Normal case: full URL already stored.
  if (isSafeAvatarUrl(s)) return s;

  // EchoVR/Discord integration sometimes stores the Discord avatar hash in the avatar_url field.
  // If so, expand it to a CDN URL using discord/custom id.
  const did = discordIdFromObj(u) || String(fallbackDiscordId || '').trim();
  if (did && looksLikeDiscordAvatarHash(s)) {
    return discordAvatarCdnUrl(did, s, 128);
  }

  return '';
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

async function prefetchAvatarsForUserIds(userIds, targetMapRef, discordIdByUserId) {
  const ids = Array.from(new Set(userIds.map((x) => String(x || '').trim()).filter(Boolean)));
  if (ids.length === 0) return;

  // Be conservative with URL length; fetch in batches.
  const batches = chunkArray(ids, 50);
  for (const batch of batches) {
    try {
      const qs = batch.map((id) => `ids=${encodeURIComponent(id)}`).join('&');
      const res = await apiGet(`/user?${qs}`);
      if (!res.ok) continue;
      const data = await res.json().catch(() => ({}));
      const users = Array.isArray(data?.users) ? data.users : [];
      if (users.length === 0) continue;

      const next = new Map(targetMapRef.value);
      for (const u of users) {
        const uid = String(u?.id || '').trim();
        if (!uid) continue;
        const k = normalizeUserId(uid);
        const fallbackDid = discordIdByUserId && typeof discordIdByUserId.get === 'function' ? discordIdByUserId.get(k) : '';
        const url = avatarUrlFromUser(u, fallbackDid);
        if (!url) continue;
        next.set(k, url);
      }
      targetMapRef.value = next;
    } catch (_) {
      // Non-fatal.
    }
  }
}

function onlineAvatarUrlForPlayer(player) {
  if (!player || typeof player !== 'object') return '';

  // Prefer avatar URL if present directly in match label player object.
  const direct = avatarUrlFromUser(player);
  if (direct) return direct;

  const uidRaw = player.user_id || player.userId;
  const uid = normalizeUserId(uidRaw);
  if (!uid || uid === ZERO_UUID) return '';
  if (onlineAvatarErrorUserIds.value.has(uid)) return '';
  return onlineAvatarsByUserId.value.get(uid) || '';
}

function onlineAvatarFallbackForPlayer(player) {
  const name = displayNameForPlayer(player) || discordUsernameForPlayer(player) || '';
  return avatarFallbackText({ username: name });
}

function onlineAvatarAltForPlayer(player) {
  const name = displayNameForPlayer(player) || discordUsernameForPlayer(player) || 'Player';
  return `${name} avatar`;
}

function onlineAvatarTitleForPlayer(player) {
  const name = displayNameForPlayer(player) || discordUsernameForPlayer(player) || '';
  return name ? name : 'Player';
}

function markOnlineAvatarError(player) {
  const uid = normalizeUserId(player?.user_id || player?.userId);
  if (!uid || uid === ZERO_UUID) return;
  const next = new Set(onlineAvatarErrorUserIds.value);
  next.add(uid);
  onlineAvatarErrorUserIds.value = next;
}

function avatarFallbackText(u) {
  const primary = String(u?.displayName || u?.display_name || u?.username || '').trim();
  if (!primary) return '?';
  const parts = primary.split(/\s+/).filter(Boolean);
  const first = parts[0] || '';
  const second = parts.length > 1 ? parts[1] : '';
  const a = first.slice(0, 1);
  const b = second.slice(0, 1);
  const out = (a + b).toUpperCase();
  return out || primary.slice(0, 1).toUpperCase() || '?';
}

const playerAvatarUrlCandidate = computed(() => avatarUrlFromUser(user.value, resolvedDiscordId.value));

const playerAvatarUrl = computed(() => {
  if (avatarLoadError.value) return '';
  return playerAvatarUrlCandidate.value;
});

const playerAvatarAlt = computed(() => {
  const u = user.value;
  const name = String(u?.displayName || u?.display_name || u?.username || 'Player').trim();
  return `${name} avatar`;
});

const playerAvatarFallback = computed(() => avatarFallbackText(user.value));

const playerAvatarTitle = computed(() => {
  const u = user.value;
  const name = String(u?.displayName || u?.display_name || u?.username || '').trim();
  return name ? name : 'Player';
});

const debugAvatars = computed(() => String(route.query?.debugAvatars || '').trim() === '1');

function normalizedFilterText(s) {
  return String(s || '').trim().toLowerCase();
}

function includesFilter(haystack, needle) {
  if (!needle) return true;
  return String(haystack || '').toLowerCase().includes(needle);
}

function sectionVisible(key) {
  if (selectedSection.value === 'all') return true;
  return selectedSection.value === key;
}

function handleSectionChange() {
  const map = {
    account: sectionAccount,
    alternates: sectionAlternates,
    enforcement: sectionEnforcement,
    pastNames: sectionPastNames,
    servers: sectionServers,
  };

  const refEl = map[selectedSection.value]?.value;
  if (refEl && typeof refEl.scrollIntoView === 'function') {
    refEl.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }
}

const onlinePartyGroups = computed(() => {
  const q = normalizedFilterText(onlinePlayersFilter.value);
  const playersRaw = Array.isArray(onlinePlayers.value) ? onlinePlayers.value : [];
  const players = !q
    ? playersRaw
    : playersRaw.filter((p) => {
        const blob = [
          displayNameForPlayer(p),
          discordUsernameForPlayer(p),
          p?.user_id,
          p?.discord_id,
          p?.evr_id,
          p?.party_id,
          p?._mode,
        ]
          .filter(Boolean)
          .join(' | ');
        return includesFilter(blob, q);
      });
  const byParty = new Map();
  const solo = [];

  const normalizeUuid = (s) => String(s || '').trim().toLowerCase();

  for (const p of players) {
    const uid = normalizeUuid(p?.user_id || p?.userId);
    const pidRaw = String(p?.party_id || p?.partyId || '').trim();
    const pidNorm = normalizeUuid(pidRaw);
    const hasRealPartyId = pidRaw.length > 0 && pidNorm !== ZERO_UUID;

    // Treat the all-zero UUID as an "unknown/solo" player (never party-group).
    // Also treat party_id=ZERO_UUID as "no party".
    if (!hasRealPartyId || uid === ZERO_UUID) {
      solo.push(p);
      continue;
    }

    const list = byParty.get(pidRaw) || [];
    list.push(p);
    byParty.set(pidRaw, list);
  }

  // Only keep party groups that have 2+ online players.
  const partyGroups = [];
  for (const [partyId, members] of byParty.entries()) {
    if ((members?.length || 0) >= 2) {
      partyGroups.push([partyId, members]);
    } else if (members?.length) {
      solo.push(members[0]);
    }
  }

  const out = [];
  partyGroups.sort((a, b) => (b[1]?.length || 0) - (a[1]?.length || 0));
  for (const [partyId, members] of partyGroups) {
    out.push({ key: `party:${partyId}`, title: 'Party', partyId, players: members });
  }

  if (solo.length) out.push({ key: 'solo', title: 'Solo', partyId: '', players: solo });
  return out;
});

function onlineLookupParamForPlayer(player) {
  if (!player || typeof player !== 'object') return null;
  const uid = player.user_id || player.userId;
  if (uid && String(uid) !== ZERO_UUID) return { forceKey: 'user_id', forceValue: String(uid) };
  const did = player.discord_id || player.discordId;
  if (did) return { forceKey: 'discord_id', forceValue: String(did) };
  const xp = player.evr_id || player.evrId || player.xp_id || player.xpId;
  if (xp) return { forceKey: 'xp_id', forceValue: String(xp) };
  const un = player.username;
  if (un) return { forceKey: 'username', forceValue: String(un) };
  return null;
}

function lookupOnlinePlayer(player) {
  const forced = onlineLookupParamForPlayer(player);
  if (!forced) return;
  closeAutocomplete();
  userId.value = String(forced.forceValue);
  lookupPlayer(forced);
}

async function fetchOnlinePlayers() {
  loadingOnlinePlayers.value = true;
  onlinePlayersError.value = '';

  try {
    const res = await apiGet('/match?limit=100&authoritative=true');
    if (!res.ok) throw new Error('Failed to fetch matches.');
    const data = await res.json();
    const matches = data.matches || [];

    const byUserId = new Map();
    const didByUid = new Map();
    const withoutUserId = [];

    for (const match of matches) {
      const raw = match?.label?.value ?? match?.label;
      if (!raw) continue;
      let label;
      try {
        label = typeof raw === 'string' ? JSON.parse(raw) : raw;
      } catch (_) {
        continue;
      }
      if (!label || typeof label !== 'object') continue;

      const mode = label?.mode;
      const players = Array.isArray(label.players) ? label.players : [];
      for (const p of players) {
        if (!p || typeof p !== 'object') continue;
        const uid = String(p.user_id || p.userId || '').trim();
        const did = String(p.discord_id || p.discordId || '').trim();
        const enriched = { ...p, _mode: mode };
        // The all-zero UUID is used by some sessions and should not be treated as a real unique ID.
        if (uid && uid !== ZERO_UUID) {
          byUserId.set(uid, enriched);
          if (did) didByUid.set(uid.toLowerCase(), did);
        } else {
          withoutUserId.push(enriched);
        }
      }
    }

    onlinePlayers.value = [...Array.from(byUserId.values()), ...withoutUserId];
    onlineDiscordIdsByUserId.value = didByUid;

    // Best-effort: prefetch avatars for users that have a real Nakama user_id.
    const ids = Array.from(byUserId.keys()).filter((uid) => uid && uid.toLowerCase() !== ZERO_UUID);
    // Only fetch IDs we don't already have cached.
    const missing = ids.filter((uid) => {
      const k = uid.toLowerCase();
      return !onlineAvatarsByUserId.value.has(k) && !onlineAvatarErrorUserIds.value.has(k);
    });
    void prefetchAvatarsForUserIds(missing, onlineAvatarsByUserId, onlineDiscordIdsByUserId.value);
  } catch (e) {
    onlinePlayersError.value = e?.message || 'Failed to load online players.';
    onlinePlayers.value = [];
  } finally {
    loadingOnlinePlayers.value = false;
  }
}

function closeAutocomplete() {
  showAutocomplete.value = false;
  autocompleteResults.value = [];
  selectedAutocompleteIndex.value = -1;
}

function isUuidLike(s) {
  return /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(String(s || '').trim());
}

function isDiscordIdLike(s) {
  return /^[0-9]{19,20}$/.test(String(s || '').trim());
}

function isXpidLike(s) {
  const trimmed = String(s || '').trim();
  return /^[A-Z0-9]+-[A-Z0-9-]+$/i.test(trimmed) && trimmed.includes('-');
}

function optionKey(kind, key, value) {
  return `${kind}:${key}:${String(value)}`;
}

function makeActionOption({ badge, badgeClass, primary, secondary, hint, queryKey, queryValue }) {
  return {
    kind: 'action',
    key: optionKey('action', queryKey, queryValue),
    badge,
    badgeClass,
    primary,
    secondary,
    hint,
    queryKey,
    queryValue,
  };
}

function makeMatchOption(item) {
  const display = String(item?.display_name || '').trim();
  const username = String(item?.username || '').trim();
  const uid = String(item?.user_id || '').trim();
  const gidRaw = item?.group_id ?? item?.groupId ?? item?.groupID ?? '';
  const gid = String(gidRaw || '').trim();

  const groupPart = gid ? ` • Guild: ${groupName(gid)}` : '';

  return {
    kind: 'match',
    key: optionKey('match', uid, display || username || uid),
    badge: 'MATCH',
    badgeClass: 'bg-slate-800 text-slate-200 border-slate-700/60',
    primary: display || username || uid,
    secondary: display && username ? username : '',
    hint: uid ? `Nakama ID: ${uid}${groupPart}` : groupPart.replace(/^ • /, ''),
    queryKey: 'user_id',
    queryValue: uid,
  };
}

function normalizeForMatch(s) {
  return String(s || '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, ' ')
    .trim();
}

function levenshteinDistance(a, b, maxDistance) {
  // Small, allocation-light Levenshtein with optional early-exit.
  const s = String(a || '');
  const t = String(b || '');
  const n = s.length;
  const m = t.length;
  if (n === 0) return m;
  if (m === 0) return n;

  if (typeof maxDistance === 'number' && Math.abs(n - m) > maxDistance) return maxDistance + 1;

  const v0 = new Array(m + 1);
  const v1 = new Array(m + 1);
  for (let j = 0; j <= m; j++) v0[j] = j;

  for (let i = 0; i < n; i++) {
    v1[0] = i + 1;
    let rowMin = v1[0];
    const si = s.charCodeAt(i);

    for (let j = 0; j < m; j++) {
      const cost = si === t.charCodeAt(j) ? 0 : 1;
      const del = v0[j + 1] + 1;
      const ins = v1[j] + 1;
      const sub = v0[j] + cost;
      const val = Math.min(del, ins, sub);
      v1[j + 1] = val;
      if (val < rowMin) rowMin = val;
    }

    if (typeof maxDistance === 'number' && rowMin > maxDistance) return maxDistance + 1;

    for (let j = 0; j <= m; j++) v0[j] = v1[j];
  }

  return v0[m];
}

function similarityScore(input, candidate) {
  const a = normalizeForMatch(input);
  const b = normalizeForMatch(candidate);
  if (!a || !b) return 0;
  if (a === b) return 1;

  // Fast-path: substring and token containment should count as a match.
  // This helps short queries (e.g. partial names) and cases where the candidate has extra suffix/prefix.
  if (b.includes(a)) {
    // Prefer earlier matches and closer length ratio.
    const ratio = Math.min(1, a.length / Math.max(1, b.length));
    return 0.75 + 0.25 * ratio;
  }
  if (a.includes(b) && b.length >= 3) {
    const ratio = Math.min(1, b.length / Math.max(1, a.length));
    return 0.65 + 0.2 * ratio;
  }

  // Token overlap: if all query tokens appear somewhere in candidate tokens, treat as a match.
  const aTokens = a.split(/\s+/).filter(Boolean);
  const bTokens = b.split(/\s+/).filter(Boolean);
  if (aTokens.length > 1 && bTokens.length > 0) {
    const hit = aTokens.filter((t) => t.length >= 2 && bTokens.some((bt) => bt.includes(t) || t.includes(bt)));
    if (hit.length === aTokens.length) {
      const ratio = Math.min(1, a.length / Math.max(1, b.length));
      return 0.7 + 0.2 * ratio;
    }
  }

  const maxLen = Math.max(a.length, b.length);
  // For performance, cap early-exit distance at ~40% of max length.
  const maxDistance = Math.max(2, Math.floor(maxLen * 0.4));
  const d = levenshteinDistance(a, b, maxDistance);
  if (d > maxDistance) return 0;
  return 1 - d / maxLen;
}

function bestCandidateScore(input, matchItem) {
  const display = matchItem?.display_name || '';
  const username = matchItem?.username || '';
  const s1 = similarityScore(input, display);
  const s2 = similarityScore(input, username);
  return Math.max(s1, s2);
}

async function searchDisplayNameMatches(input) {
  const trimmed = String(input || '').trim();
  if (!trimmed || trimmed.length < 2) return [];

  // Server-side search behavior may be case-sensitive depending on implementation.
  // To ensure suggestions regardless of input casing, query with multiple case variants
  // and then rank results locally using case-insensitive similarity scoring.
  const normalized = trimmed.toLowerCase();
  const upper = trimmed.toUpperCase();

  const tokens = normalized.split(/\s+/).filter((t) => t.length >= 2);
  const longestToken = tokens.sort((a, b) => b.length - a.length)[0] || normalized;

  // Server-side search only returns substring matches on display_name.
  // To support "close" suggestions, we query with short seeds, then rank locally.
  const seeds = [];
  // Raw input plus case variants.
  seeds.push(trimmed);
  seeds.push(normalized);
  seeds.push(upper);

  // Token-based seeds (normalized), plus short prefixes.
  if (longestToken && longestToken !== normalized) seeds.push(longestToken);
  if (normalized.length >= 3) {
    seeds.push(normalized.slice(0, 3));
    seeds.push(upper.slice(0, 3));
  }
  if (longestToken.length >= 3) {
    seeds.push(longestToken.slice(0, 3));
  }
  if (normalized.length >= 2) {
    seeds.push(normalized.slice(0, 2));
    seeds.push(upper.slice(0, 2));
  }

  const uniqueSeeds = Array.from(new Set(seeds.map((s) => String(s || '').trim()).filter(Boolean))).slice(0, 6);

  try {
    const responses = await Promise.all(
      uniqueSeeds.map(async (seed) => {
        const res = await apiGet(`/rpc/account/search?display_name=${encodeURIComponent(seed)}&limit=25`);
        if (!res.ok) return [];
        const data = await res.json().catch(() => ({}));
        return Array.isArray(data.display_name_matches) ? data.display_name_matches : [];
      })
    );

    const merged = responses.flat();
    const byUserId = new Map();

    for (const item of merged) {
      const uid = String(item?.user_id || '').trim();
      if (!uid) continue;

      const prev = byUserId.get(uid);
      const nextScore = bestCandidateScore(trimmed, item);
      if (!prev || nextScore > prev._score) {
        byUserId.set(uid, { ...item, _score: nextScore });
      }
    }

    const ranked = Array.from(byUserId.values())
      .filter((x) => x._score > 0)
      .sort((a, b) => {
        if (b._score !== a._score) return b._score - a._score;
        const aT = a.updated_at ? new Date(a.updated_at).getTime() : 0;
        const bT = b.updated_at ? new Date(b.updated_at).getTime() : 0;
        return bT - aT;
      })
      .slice(0, 10);

    return ranked.map(makeMatchOption);
  } catch (e) {
    console.warn('Autocomplete search failed:', e);
    return [];
  }
}

function onSearchInput() {
  // Clear existing timeout
  if (autocompleteTimeout) {
    clearTimeout(autocompleteTimeout);
  }

  // Reset selection
  selectedAutocompleteIndex.value = -1;

  const input = String(userId.value || '').trim();

  // For IDs, show immediate single-click lookup options without calling the server.
  const immediate = [];
  if (input) {
    if (isUuidLike(input)) {
      immediate.push(
        makeActionOption({
          badge: 'ID',
          badgeClass: 'bg-sky-900/60 text-sky-200 border-sky-700/40',
          primary: input,
          secondary: '',
          hint: 'Lookup by Nakama ID',
          queryKey: 'user_id',
          queryValue: input,
        })
      );
    } else if (isDiscordIdLike(input)) {
      immediate.push(
        makeActionOption({
          badge: 'DISCORD',
          badgeClass: 'bg-indigo-900/60 text-indigo-200 border-indigo-700/40',
          primary: input,
          secondary: '',
          hint: 'Lookup by Discord ID',
          queryKey: 'discord_id',
          queryValue: input,
        })
      );
    } else if (isXpidLike(input)) {
      immediate.push(
        makeActionOption({
          badge: 'EVRID',
          badgeClass: 'bg-emerald-900/60 text-emerald-200 border-emerald-700/40',
          primary: input,
          secondary: '',
          hint: 'Lookup by EVR/XPID',
          queryKey: 'xp_id',
          queryValue: input,
        })
      );
    }
  }

  if (immediate.length) {
    autocompleteResults.value = immediate;
    showAutocomplete.value = true;
    selectedAutocompleteIndex.value = 0;
    return;
  }

  // Debounce remote search for display-name matches.
  autocompleteTimeout = setTimeout(async () => {
    const trimmed = String(userId.value || '').trim();
    if (!trimmed || trimmed.length < 2) {
      closeAutocomplete();
      return;
    }

    const matches = await searchDisplayNameMatches(trimmed);

    const actions = [
      makeActionOption({
        badge: 'USERNAME',
        badgeClass: 'bg-slate-800 text-slate-200 border-slate-700/60',
        primary: trimmed,
        secondary: '',
        hint: 'Try exact username lookup',
        queryKey: 'username',
        queryValue: trimmed,
      }),
      makeActionOption({
        badge: 'DISPLAY',
        badgeClass: 'bg-slate-800 text-slate-200 border-slate-700/60',
        primary: trimmed,
        secondary: '',
        hint: 'Try exact display name lookup',
        queryKey: 'display_name',
        queryValue: trimmed,
      }),
    ];

    autocompleteResults.value = [...matches, ...actions];
    showAutocomplete.value = autocompleteResults.value.length > 0;
    selectedAutocompleteIndex.value = showAutocomplete.value ? 0 : -1;
  }, 250);
}

function selectResult(result) {
  if (!result) return;
  closeAutocomplete();
  const key = result.queryKey;
  const value = result.queryValue;
  if (key && typeof value !== 'undefined') {
    userId.value = String(value);
    lookupPlayer({ forceKey: key, forceValue: String(value) });
    return;
  }
  lookupPlayer();
}

function hideAutocompleteDelayed() {
  // Delay hiding to allow click events to fire
  setTimeout(() => {
    closeAutocomplete();
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

function handleSearchEnter() {
  if (showAutocomplete.value && selectedAutocompleteIndex.value >= 0) {
    selectAutocompleteItem();
    return;
  }
  closeAutocomplete();
  lookupPlayer();
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

async function lookupPlayer(forced) {
  error.value = '';
  user.value = null;
  loginHistory.value = null;
  journal.value = null;
  displayNameHistory.value = null;
  gameServers.value = [];
  ownedServers.value = [];
  document.title = 'Player Lookup'; // Reset title during lookup
  loading.value = true;
  try {
    const input = userId.value.trim();
    if (!input) throw new Error('Please enter a username, XPID, User ID, or Discord ID.');

    async function doLookup(paramKey, paramValue) {
      const lookupRes = await apiGet(`/rpc/account/lookup?${paramKey}=${encodeURIComponent(paramValue)}`);
      const data = await lookupRes.json().catch(() => ({}));
      if (!lookupRes.ok) {
        const message = data?.message || 'User not found.';
        const err = new Error(message);
        err.status = lookupRes.status;
        err.payload = data;
        throw err;
      }
      return data;
    }

    let lookupData;
    if (forced?.forceKey) {
      lookupData = await doLookup(forced.forceKey, forced.forceValue);
    } else {
      // First, resolve the identifier to a user ID.
      const identifierParam = detectIdentifierType(input);
      const paramKey = Object.keys(identifierParam)[0];
      const paramValue = identifierParam[paramKey];

      if (paramKey !== 'username') {
        lookupData = await doLookup(paramKey, paramValue);
      } else {
        // Username is ambiguous with display name. Try username exact first, then display_name exact.
        try {
          lookupData = await doLookup('username', String(paramValue));
        } catch (e) {
          // Only fall back when it looks like a not-found; otherwise surface the original error.
          if (e?.status === 404 || /not found/i.test(String(e?.message || ''))) {
            try {
              lookupData = await doLookup('display_name', String(paramValue));
            } catch (e2) {
              if (e2?.status === 404 || /not found/i.test(String(e2?.message || ''))) {
                // Final fallback: partial-name resolution via account/search.
                const matches = await searchDisplayNameMatches(String(paramValue));
                const best = matches?.[0];
                if (best?.queryKey === 'user_id' && best?.queryValue) {
                  lookupData = await doLookup('user_id', String(best.queryValue));
                } else {
                  throw e2;
                }
              } else {
                throw e2;
              }
            }
          } else {
            throw e;
          }
        }
      }
    }

    const uid = lookupData.id;

    resolvedDiscordId.value = String(lookupData.discord_id || lookupData.discordId || '');

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

    // Fetch active game(s) for this player
    await fetchGameServers({ userId: uid, discordId: resolvedDiscordId.value });
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

function labelHasUser(label, ids) {
  if (!label || typeof label !== 'object') return false;
  if (!ids || typeof ids !== 'object') return false;

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

// Fetch active game(s) for the user (current session), NOT servers they own.
async function fetchGameServers(target) {
  loadingServers.value = true;
  const previousServers = gameServers.value;
  const previousOwned = ownedServers.value;

  try {
    // Fetch all matches - the backend AfterListMatches hook will filter based on permissions
    const res = await apiGet('/match?limit=100&authoritative=true');
    if (!res.ok) {
      console.warn('Failed to fetch matches');
      return;
    }

    const data = await res.json();
    const matches = data.matches || [];

    const targetUserId = typeof target === 'string' ? target : target?.userId;
    const targetDiscordId = typeof target === 'string' ? '' : target?.discordId;

    // Filter to matches where this user appears in the match label player list
    const active = [];
    const owned = [];
    for (const match of matches) {
      try {
        const raw = match?.label?.value ?? match?.label;
        if (!raw) continue;

        const label = typeof raw === 'string' ? JSON.parse(raw) : raw;
        if (!label || typeof label !== 'object') continue;

        if (!Array.isArray(label.players)) label.players = [];
        if (typeof label.player_count !== 'number') label.player_count = label.players.length;

        if (labelHasUser(label, { userId: targetUserId, discordId: targetDiscordId })) {
          active.push({
            match_id: match.match_id,
            label,
          });
        }

        const operatorId = label.broadcaster?.oper || label.broadcaster?.operator_id || label.game_server?.operator_id;
        if (targetUserId && operatorId && String(operatorId) === String(targetUserId)) {
          owned.push({
            match_id: match.match_id,
            label,
          });
        }
      } catch (e) {
        console.warn('Failed to parse match label:', e);
      }
    }

    gameServers.value = active;
    ownedServers.value = owned;
  } catch (e) {
    console.warn('Failed to fetch game servers:', e);
    // Keep prior list if refresh fails.
    gameServers.value = previousServers;
    ownedServers.value = previousOwned;
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

function serverSearchBlob(server) {
  const label = server?.label || {};
  const parts = [];
  parts.push(server?.match_id);
  parts.push(label?.mode);
  parts.push(label?.group_id);
  parts.push(label?.server_city);
  parts.push(label?.server_country);
  parts.push(label?.server_region);
  parts.push(label?.broadcaster?.endpoint);

  const players = Array.isArray(label?.players) ? label.players : [];
  for (const p of players) {
    parts.push(displayNameForPlayer(p));
    parts.push(discordUsernameForPlayer(p));
    parts.push(p?.user_id);
    parts.push(p?.discord_id);
    parts.push(p?.evr_id);
  }
  return parts.filter(Boolean).join(' | ');
}

const filteredGameServers = computed(() => {
  const q = normalizedFilterText(activeServersFilter.value);
  if (!q) return gameServers.value;
  return (gameServers.value || []).filter((s) => includesFilter(serverSearchBlob(s), q));
});

const filteredOwnedServers = computed(() => {
  const q = normalizedFilterText(activeServersFilter.value);
  if (!q) return ownedServers.value;
  return (ownedServers.value || []).filter((s) => includesFilter(serverSearchBlob(s), q));
});

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

const discordIdText = computed(() => {
  const fromUser = user.value?.customId || user.value?.custom_id || user.value?.customID || '';
  const fromLookup = resolvedDiscordId.value || '';
  const v = String(fromUser || fromLookup || '').trim();
  return v || '—';
});

const createdAtText = computed(() => {
  const raw = user.value?.createTime || user.value?.create_time || user.value?.createAt || '';
  const v = String(raw || '').trim();
  return v ? formatDate(v) : '—';
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

const recentLoginsList = computed(() => {
  const hist = loginHistory.value;
  if (!hist || !hist.history) return [];

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

  const rows = Array.from(latestByXpid.entries()).map(([xpid, ts]) => ({
    xpid,
    ts,
    when: formatRelative(ts),
  }));
  rows.sort((a, b) => new Date(b.ts) - new Date(a.ts));
  return rows;
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

const filteredSuspensionsByGroup = computed(() => {
  const q = normalizedFilterText(enforcementFilter.value);
  if (!q) return suspensionsByGroup.value;

  const out = [];
  for (const grp of suspensionsByGroup.value || []) {
    const groupBlob = groupName(grp.groupId);
    const recs = (grp.records || []).filter((rec) => {
      const blob = [
        groupBlob,
        rec?.suspension_notice,
        rec?.notes,
        rec?.user_id,
        rec?.enforcer_user_id,
        rec?.enforcer_discord_id,
        getEnforcerName(rec),
        getSuspensionStatus(rec),
      ]
        .filter(Boolean)
        .join(' | ');
      return includesFilter(blob, q);
    });
    if (recs.length) out.push({ ...grp, records: recs });
  }
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

const filteredAlternateAccounts = computed(() => {
  const q = normalizedFilterText(alternateAccountsFilter.value);
  if (!q) return alternateAccounts.value;
  return (alternateAccounts.value || []).filter((alt) => {
    const blob = [alt.userId, alt.username, ...(alt.items || [])].filter(Boolean).join(' | ');
    return includesFilter(blob, q);
  });
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

const filteredPastDisplayNamesByGuild = computed(() => {
  const q = normalizedFilterText(pastDisplayNamesFilter.value);
  if (!q) return pastDisplayNamesByGuild.value;

  const out = [];
  for (const grp of pastDisplayNamesByGuild.value || []) {
    const names = (grp.names || []).filter((n) => includesFilter(n?.name, q));
    if (names.length) out.push({ ...grp, names });
  }
  return out;
});

function partyIdForUserInServer(server, ids) {
  const label = server?.label;
  if (!label || typeof label !== 'object') return '';
  const players = Array.isArray(label.players) ? label.players : [];
  const userId = ids?.userId ? String(ids.userId) : '';
  const discordId = ids?.discordId ? String(ids.discordId) : '';
  if (!userId && !discordId) return '';

  for (const p of players) {
    if (!p || typeof p !== 'object') continue;
    const pid = p.user_id || p.userId || p.nakama_id || p.nakamaId || p.id;
    const did = p.discord_id || p.discordId;
    const match = (userId && pid && String(pid) === userId) || (discordId && did && String(did) === discordId);
    if (!match) continue;
    const party = p.party_id || p.partyId || '';
    return String(party || '').trim();
  }

  return '';
}

const currentPartyId = computed(() => {
  const uid = user.value?.id ? String(user.value.id) : '';
  const did = resolvedDiscordId.value ? String(resolvedDiscordId.value) : '';
  if (!uid && !did) return '';

  const servers = Array.isArray(gameServers.value) ? gameServers.value : [];
  for (const s of servers) {
    const party = partyIdForUserInServer(s, { userId: uid, discordId: did });
    if (party) return party;
  }
  return '';
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

function discordUsernameForPlayer(player) {
  if (!player || typeof player !== 'object') return '';
  // In this fork, match label PlayerInfo.Username is typically the Discord username (Nakama username from Discord auth).
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

function endpointForTaxi(endpoint) {
  if (!endpoint) return '';
  if (typeof endpoint === 'string') return endpoint.trim();
  if (typeof endpoint === 'object') {
    const externalIp = endpoint.external_ip || endpoint.externalIp || endpoint.ip;
    const port = endpoint.port;
    if (externalIp && port) return `${externalIp}:${port}`;
    if (externalIp) return String(externalIp);
  }
  return String(endpoint).trim();
}

function serverPlayerCount(server) {
  const label = server?.label;
  const fromCount = Number(label?.player_count);
  if (Number.isFinite(fromCount) && fromCount >= 0) return fromCount;
  const players = Array.isArray(label?.players) ? label.players : [];
  return players.length;
}

function shouldShowTaxi(server) {
  if (!server?.match_id) return false;
  return serverPlayerCount(server) > 0;
}

function taxiButtonTitle() {
  return 'Copy & open Echo Taxi link';
}

function taxiUrlForServer(server) {
  const matchId = server?.match_id;
  if (!matchId) return null;

  // Echo Taxi format: https://echo.taxi/spark://c/<match_id>
  return `https://echo.taxi/spark://c/${encodeURIComponent(String(matchId))}`;
}

async function handleTaxi(server) {
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
</script>

<style scoped></style>
