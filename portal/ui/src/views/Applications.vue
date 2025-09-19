<template>
  <div>
    <h2 class="text-2xl font-bold mb-4 text-white">Applications</h2>

    <!-- Application creation form and list -->
    <form @submit.prevent="createApplication">
      <div class="mb-4">
        <label class="block text-sm font-medium text-gray-300 mb-2">Name</label>
        <input
          v-model="newApp.name"
          type="text"
          required
          class="w-full px-3 py-2 bg-gray-700 text-white rounded border border-gray-600 focus:border-blue-500 focus:outline-none"
          placeholder="My Application"
        />
      </div>
      <div class="mb-4">
        <label class="block text-sm font-medium text-gray-300 mb-2">Description</label>
        <textarea
          v-model="newApp.description"
          rows="3"
          class="w-full px-3 py-2 bg-gray-700 text-white rounded border border-gray-600 focus:border-blue-500 focus:outline-none"
          placeholder="A brief description of your application"
        ></textarea>
      </div>
      <div class="flex gap-3">
        <button
          type="submit"
          :disabled="creating"
          class="px-4 py-2 bg-green-600 hover:bg-green-700 disabled:bg-gray-600 text-white rounded transition"
        >
          {{ creating ? 'Creating...' : 'Create Application' }}
        </button>
        <button
          type="button"
          @click="showCreateForm = false"
          class="px-4 py-2 bg-gray-600 hover:bg-gray-700 text-white rounded transition"
        >
          Cancel
        </button>
      </div>
    </form>
    <!-- ...existing application list... -->

    <!-- Loading State -->
    <div v-if="loading" class="text-gray-300">Loading...</div>

    <!-- Error State -->
    <div v-else-if="error" class="text-red-400">{{ error }}</div>

    <!-- Applications Grid -->
    <!-- Create Application Button -->
    <div class="mb-6">
      <button
        @click="showCreateForm = !showCreateForm"
        class="px-4 py-2 bg-blue-600 hover:bg-blue-700 text-white rounded-lg transition"
      >
        {{ showCreateForm ? 'Cancel' : 'Create Application' }}
      </button>
    </div>

    <!-- Create Application Form -->
    <div v-if="showCreateForm" class="bg-gray-800 rounded-lg p-6 mb-6">
      <h3 class="text-lg font-semibold text-white mb-4">Create New Application</h3>
      <form @submit.prevent="createApplication">
        <div class="mb-4">
          <label class="block text-sm font-medium text-gray-300 mb-2">Name</label>
          <input
            v-model="newApp.name"
            type="text"
            required
            class="w-full px-3 py-2 bg-gray-700 text-white rounded border border-gray-600 focus:border-blue-500 focus:outline-none"
            placeholder="My Application"
          />
        </div>
        <div class="mb-4">
          <label class="block text-sm font-medium text-gray-300 mb-2">Description</label>
          <textarea
            v-model="newApp.description"
            rows="3"
            class="w-full px-3 py-2 bg-gray-700 text-white rounded border border-gray-600 focus:border-blue-500 focus:outline-none"
            placeholder="A brief description of your application"
          ></textarea>
        </div>
        <div class="flex gap-3">
          <button
            type="submit"
            :disabled="creating"
            class="px-4 py-2 bg-green-600 hover:bg-green-700 disabled:bg-gray-600 text-white rounded transition"
          >
            {{ creating ? 'Creating...' : 'Create Application' }}
          </button>
          <button
            type="button"
            @click="showCreateForm = false"
            class="px-4 py-2 bg-gray-600 hover:bg-gray-700 text-white rounded transition"
          >
            Cancel
          </button>
        </div>
      </form>
    </div>

    <div>
      <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
        <div
          v-for="app in applications"
          :key="app.id"
          class="bg-gray-800 rounded-lg p-6 shadow hover:shadow-lg transition"
        >
          <h3 class="text-lg font-semibold text-white mb-2">{{ app.name }}</h3>
          <p class="text-gray-300 mb-2">{{ app.description }}</p>
          <div class="text-xs text-gray-400">ID: {{ app.id }}</div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted, computed } from 'vue';

const applications = ref([]);
const loading = ref(true);
const error = ref(null);
const githubStatus = ref({ linked: false });
const showCreateForm = ref(false);
const creating = ref(false);
const newApp = ref({ name: '', description: '' });

const API_BASE = import.meta.env.VITE_NAKAMA_API_BASE;
const token = localStorage.getItem('jwt');

async function fetchApplications() {
  if (!githubLinked.value) return;
  try {
    const res = await fetch(`${API_BASE}/v2/rpc/list_applications`, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${token}`,
        'Content-Type': 'application/json',
      },
      body: '{}',
    });
    if (!res.ok) throw new Error('Failed to fetch applications');
    const data = await res.json();
    const payload = data.payload ? JSON.parse(data.payload) : {};
    applications.value = payload.applications || [];
  } catch (e) {
    error.value = e.message;
  }
}

async function createApplication() {
  creating.value = true;
  try {
    const res = await fetch(`${API_BASE}/v2/rpc/create_application`, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${token}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        name: newApp.value.name,
        description: newApp.value.description,
      }),
    });

    if (!res.ok) {
      const errorData = await res.json();
      throw new Error(errorData.message || 'Failed to create application');
    }

    // Reset form and refresh applications
    newApp.value = { name: '', description: '' };
    showCreateForm.value = false;
    await fetchApplications();
  } catch (e) {
    error.value = e.message;
  } finally {
    creating.value = false;
  }
}

onMounted(async () => {
  loading.value = true;
  error.value = null;

  try {
    await fetchApplications();
  } catch (e) {
    error.value = e.message;
  } finally {
    loading.value = false;
  }
});
</script>
