<template>
  <AppLayout>
    <div class="mx-auto max-w-5xl">
      <!-- Page Intro -->
      <div class="mb-8">
        <h2 class="text-xl font-semibold text-gray-900 dark:text-white">
          {{ t('aiImage.directoryTitle') }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">
          {{ t('aiImage.directoryDescription') }}
        </p>
      </div>

      <!-- Feature Cards -->
      <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <component
          :is="entry.to ? 'router-link' : 'div'"
          v-for="entry in visibleEntries"
          :key="entry.key"
          :to="entry.to"
          class="group flex flex-col rounded-2xl border border-gray-200 bg-white p-5 shadow-sm transition-all hover:border-primary-300 hover:shadow-md dark:border-dark-700 dark:bg-dark-800 dark:hover:border-primary-600"
          :class="{ 'cursor-pointer': entry.to }"
        >
          <div class="mb-4 flex h-11 w-11 items-center justify-center rounded-xl bg-primary-50 text-primary-600 transition-colors group-hover:bg-primary-100 dark:bg-primary-900/30 dark:text-primary-400">
            <component :is="entry.icon" class="h-6 w-6" />
          </div>
          <h3 class="text-base font-semibold text-gray-900 dark:text-white">
            {{ t(`aiImage.entries.${entry.key}.title`) }}
          </h3>
          <p class="mt-1.5 flex-1 text-sm text-gray-500 dark:text-dark-400">
            {{ t(`aiImage.entries.${entry.key}.description`) }}
          </p>
          <div v-if="entry.to" class="mt-4 flex items-center gap-1 text-sm font-medium text-primary-600 dark:text-primary-400">
            <span>{{ t('aiImage.enter') }}</span>
            <svg class="h-4 w-4 transition-transform group-hover:translate-x-0.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
              <path stroke-linecap="round" stroke-linejoin="round" d="M13.5 4.5L21 12m0 0l-7.5 7.5M21 12H3" />
            </svg>
          </div>
        </component>
      </div>

      <!-- Empty State (no entries visible) -->
      <div
        v-if="visibleEntries.length === 0"
        class="rounded-2xl border border-dashed border-gray-300 bg-white p-12 text-center dark:border-dark-600 dark:bg-dark-800"
      >
        <p class="text-sm text-gray-500 dark:text-dark-400">
          {{ t('aiImage.empty') }}
        </p>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, h, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import { useBatchImageAccess } from '@/composables/useBatchImageAccess'

const { t } = useI18n()
const { canUseBatchImage } = useBatchImageAccess()

// Inline SVG icons (kept local to keep the directory page self-contained)
const BatchImageIcon: Component = {
  render: () =>
    h(
      'svg',
      { fill: 'none', viewBox: '0 0 24 24', stroke: 'currentColor', 'stroke-width': '1.5' },
      [
        h('path', {
          'stroke-linecap': 'round',
          'stroke-linejoin': 'round',
          d: 'M6.827 6.175A2.31 2.31 0 015.186 7.23c-.38.054-.757.112-1.134.175C2.999 7.58 2.25 8.507 2.25 9.574V18a2.25 2.25 0 002.25 2.25h15A2.25 2.25 0 0021.75 18V9.574c0-1.067-.75-1.994-1.802-2.169a47.865 47.865 0 00-1.134-.175 2.31 2.31 0 01-1.64-1.055l-.822-1.316a2.25 2.25 0 00-1.906-1.059H9.554a2.25 2.25 0 00-1.906 1.059l-.821 1.316z'
        }),
        h('path', {
          'stroke-linecap': 'round',
          'stroke-linejoin': 'round',
          d: 'M16.5 12.75a4.5 4.5 0 11-9 0 4.5 4.5 0 019 0zM18.75 10.5h.008v.008h-.008V10.5z'
        })
      ]
    )
}

const GenerateIcon: Component = {
  render: () =>
    h(
      'svg',
      { fill: 'none', viewBox: '0 0 24 24', stroke: 'currentColor', 'stroke-width': '1.5' },
      [
        h('path', {
          'stroke-linecap': 'round',
          'stroke-linejoin': 'round',
          d: 'M9.813 15.904L9 18.75l-.813-2.846a4.5 4.5 0 00-3.09-3.09L2.25 12l2.846-.813a4.5 4.5 0 003.09-3.09L9 5.25l.813 2.846a4.5 4.5 0 003.09 3.09L15.75 12l-2.846.813a4.5 4.5 0 00-3.09 3.09zM18.259 8.715L18 9.75l-.259-1.035a3.375 3.375 0 00-2.456-2.456L14.25 6l1.035-.259a3.375 3.375 0 002.456-2.456L18 2.25l.259 1.035a3.375 3.375 0 002.456 2.456L21.75 6l-1.035.259a3.375 3.375 0 00-2.456 2.456z'
        })
      ]
    )
}

interface AiImageEntry {
  key: string
  to?: string
  icon: Component
  /** Returns false to hide the entry (e.g. when the feature is gated off). */
  visible?: () => boolean
}

// Configuration-driven directory entries. Add new AI image features here.
const entries: AiImageEntry[] = [
  {
    key: 'generate',
    to: '/ai-image/workspace',
    icon: GenerateIcon,
  },
  {
    key: 'batchImage',
    to: '/batch-image',
    icon: BatchImageIcon,
    visible: () => canUseBatchImage.value,
  },
]

const visibleEntries = computed(() => entries.filter((e) => !e.visible || e.visible()))
</script>
