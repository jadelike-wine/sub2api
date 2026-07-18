<template>
  <span
    class="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium"
    :class="badgeClass"
  >
    <span class="h-1.5 w-1.5 rounded-full" :class="dotClass" />
    {{ label }}
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ImageGenerationStatus } from '@/types'

const props = defineProps<{
  status: ImageGenerationStatus
}>()

const { t } = useI18n()

const label = computed(() => t(`aiImage.workspace.status.${props.status}`))

const badgeClass = computed(() => {
  switch (props.status) {
    case 'pending':
      return 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-dark-200'
    case 'processing':
      return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
    case 'succeeded':
      return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
    case 'failed':
      return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
    case 'canceled':
      return 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-dark-400'
    default:
      return 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-dark-200'
  }
})

const dotClass = computed(() => {
  switch (props.status) {
    case 'pending':
    case 'canceled':
      return 'bg-gray-400'
    case 'processing':
      return 'bg-blue-500 animate-pulse'
    case 'succeeded':
      return 'bg-green-500'
    case 'failed':
      return 'bg-red-500'
    default:
      return 'bg-gray-400'
  }
})
</script>
