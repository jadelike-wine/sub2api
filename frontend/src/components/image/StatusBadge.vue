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
import { getImageStatusI18nKey } from '@/views/user/imageStatusMessages'

const props = defineProps<{
  status: ImageGenerationStatus | string
}>()

const { t } = useI18n()

// 使用映射表查询 i18n key，未知状态统一回退到 aiImage.workspace.status.unknown，
// 避免直接拼接 status 导致前端显示原始 key 字符串（如 "aiImage.workspace.status.queued"）。
const label = computed(() => t(getImageStatusI18nKey(props.status)))

const badgeClass = computed(() => {
  switch (props.status) {
    case 'queued':
    case 'pending':
      return 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-dark-200'
    case 'processing':
      return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
    case 'succeeded':
    case 'completed':
      return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
    case 'failed':
    case 'timeout':
      return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
    case 'canceled':
      return 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-dark-400'
    default:
      return 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-dark-200'
  }
})

const dotClass = computed(() => {
  switch (props.status) {
    case 'queued':
    case 'pending':
    case 'canceled':
      return 'bg-gray-400'
    case 'processing':
      return 'bg-blue-500 animate-pulse'
    case 'succeeded':
    case 'completed':
      return 'bg-green-500'
    case 'failed':
    case 'timeout':
      return 'bg-red-500'
    default:
      return 'bg-gray-400'
  }
})
</script>
