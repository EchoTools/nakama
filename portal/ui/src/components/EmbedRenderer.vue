<template>
  <div>
    <div v-for="(embed, i) in embeds" :key="i" class="mb-8">
      <div
        class="rounded-lg shadow-lg p-6 mb-2"
        :style="{
          background: embed.color ? `#${embed.color.toString(16).padStart(6, '0')}` : '#23272a',
          color: '#fff',
        }"
      >
        <div v-if="embed.author" class="mb-2 flex items-center">
          <span v-if="embed.author.icon_url" class="mr-2">
            <img :src="embed.author.icon_url" class="w-6 h-6 rounded-full inline-block" />
          </span>
          <span class="font-semibold">{{ embed.author.name }}</span>
        </div>
        <div v-if="embed.title" class="text-lg font-bold mb-1">{{ embed.title }}</div>
        <div v-if="embed.description" class="mb-2 whitespace-pre-line">{{ embed.description }}</div>
        <div v-if="embed.fields && embed.fields.length" class="mb-2">
          <div v-for="(field, j) in embed.fields" :key="j" class="mb-1">
            <span class="font-semibold">{{ field.name }}:</span>
            <span v-if="field.inline" class="ml-2">{{ field.value }}</span>
            <div v-else class="ml-2">{{ field.value }}</div>
          </div>
        </div>
        <div v-if="embed.footer" class="text-xs text-gray-300 mt-2">{{ embed.footer.text }}</div>
      </div>
    </div>
  </div>
</template>

<script setup>
const props = defineProps({
  embeds: Array,
});
</script>

<style scoped></style>
