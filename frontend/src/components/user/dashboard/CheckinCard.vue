<template>
  <div v-if="visible" class="card overflow-hidden">
    <div class="relative bg-gradient-to-br from-amber-500 to-orange-500 px-6 py-5 dark:from-amber-600 dark:to-orange-600">
      <!-- Background decoration -->
      <div class="pointer-events-none absolute right-4 top-4 opacity-20">
        <svg class="h-20 w-20 text-white" fill="none" stroke="currentColor" viewBox="0 0 24 24">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5"
            d="M9 12.75L11.25 15 15 9.75M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
        </svg>
      </div>

      <div class="relative">
        <div class="flex items-center gap-3">
          <div class="flex h-10 w-10 items-center justify-center rounded-xl bg-white/20 backdrop-blur-sm">
            <svg class="h-6 w-6 text-white" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
                d="M9 12.75L11.25 15 15 9.75M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
            </svg>
          </div>
          <div>
            <h2 class="text-lg font-bold text-white">{{ t('dashboard.checkin.title') }}</h2>
            <p class="text-sm text-amber-50">{{ t('dashboard.checkin.description') }}</p>
          </div>
        </div>
      </div>
    </div>

    <div class="p-6">
      <!-- Loading state -->
      <div v-if="loading" class="flex items-center justify-center py-6">
        <div class="h-6 w-6 animate-spin rounded-full border-2 border-primary-200 border-t-primary-600"></div>
      </div>

      <template v-else>
        <!-- Error state -->
        <div v-if="error" class="py-4 text-center text-sm text-gray-500 dark:text-gray-400">
          {{ error }}
        </div>

        <template v-else>
          <!-- Status info -->
          <div class="mb-4 flex items-center justify-between">
            <div class="flex items-center gap-2">
              <span
                class="inline-flex items-center gap-1.5 rounded-full px-3 py-1 text-xs font-medium"
                :class="status?.checked_in_today
                  ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
                  : 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'"
              >
                <span
                  class="h-1.5 w-1.5 rounded-full"
                  :class="status?.checked_in_today ? 'bg-green-500' : 'bg-amber-500'"
                ></span>
                {{ status?.checked_in_today ? t('dashboard.checkin.checkedIn') : t('dashboard.checkin.notCheckedIn') }}
              </span>
            </div>
            <span class="text-xs text-gray-400 dark:text-gray-500">{{ status?.today_date }}</span>
          </div>

          <!-- Reward range -->
          <div v-if="!status?.checked_in_today" class="mb-4 rounded-lg bg-gray-50 p-3 dark:bg-dark-800/50">
            <div class="flex items-center justify-between">
              <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('dashboard.checkin.rewardRange') }}</span>
              <span class="text-sm font-semibold text-gray-900 dark:text-white">
                ${{ formatRange(status?.min_reward || 0, status?.max_reward || 0) }}
              </span>
            </div>
          </div>

          <!-- Already checked in: show reward -->
          <div v-else class="mb-4 space-y-2 rounded-lg bg-gray-50 p-3 dark:bg-dark-800/50">
            <div class="flex items-center justify-between">
              <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('dashboard.checkin.rewardAmount') }}</span>
              <span class="text-sm font-bold text-emerald-600 dark:text-emerald-400">
                +${{ formatAmount(status?.reward_amount || 0) }}
              </span>
            </div>
            <div v-if="status?.checkin_at" class="flex items-center justify-between">
              <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('dashboard.checkin.lastCheckinAt') }}</span>
              <span class="text-xs text-gray-600 dark:text-gray-300">{{ formatTime(status.checkin_at) }}</span>
            </div>
          </div>

          <!-- Check-in result (after successful check-in) -->
          <div v-if="checkinResult" class="mb-4 space-y-2 rounded-lg border border-emerald-200 bg-emerald-50 p-3 dark:border-emerald-800 dark:bg-emerald-900/20">
            <div class="flex items-center justify-between">
              <span class="text-xs text-emerald-600 dark:text-emerald-400">{{ t('dashboard.checkin.rewardAmount') }}</span>
              <span class="text-lg font-bold text-emerald-600 dark:text-emerald-400">
                +${{ formatAmount(checkinResult.reward_amount) }}
              </span>
            </div>
            <div class="flex items-center justify-between">
              <span class="text-xs text-emerald-600 dark:text-emerald-400">{{ t('dashboard.checkin.newBalance') }}</span>
              <span class="text-sm font-semibold text-gray-900 dark:text-white">
                ${{ formatAmount(checkinResult.new_balance) }}
              </span>
            </div>
          </div>

          <!-- Action button -->
          <button
            v-if="!status?.checked_in_today && !checkinResult"
            type="button"
            class="btn btn-primary w-full"
            :disabled="checking"
            @click="handleCheckin"
          >
            <span v-if="checking" class="flex items-center gap-2">
              <div class="h-4 w-4 animate-spin rounded-full border-2 border-white/30 border-t-white"></div>
              {{ t('dashboard.checkin.checking') }}
            </span>
            <span v-else>{{ t('dashboard.checkin.checkinButton') }}</span>
          </button>

          <div v-else-if="checkinResult" class="text-center text-xs text-gray-400 dark:text-gray-500">
            {{ t('dashboard.checkin.alreadyDone') }}
          </div>
        </template>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { getCheckinStatus, checkin, type CheckinStatus, type CheckinResult } from '@/api/checkin'

const { t } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()

const loading = ref(true)
const checking = ref(false)
const error = ref('')
const status = ref<CheckinStatus | null>(null)
const checkinResult = ref<CheckinResult | null>(null)

const visible = computed(() => {
  // Only show if daily check-in is enabled in public settings
  return appStore.cachedPublicSettings?.daily_checkin_enabled === true
})

function formatAmount(n: number): string {
  return new Intl.NumberFormat('en-US', {
    minimumFractionDigits: 2,
    maximumFractionDigits: 8,
  }).format(n)
}

function formatRange(min: number, max: number): string {
  const fmt = (n: number) => new Intl.NumberFormat('en-US', {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  }).format(n)
  return `${fmt(min)} ~ ${fmt(max)}`
}

function formatTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString(undefined, {
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  })
}

async function loadStatus() {
  loading.value = true
  error.value = ''
  try {
    status.value = await getCheckinStatus()
  } catch (err) {
    error.value = t('dashboard.checkin.disabled')
  } finally {
    loading.value = false
  }
}

async function handleCheckin() {
  if (checking.value) return
  checking.value = true
  error.value = ''
  try {
    const result = await checkin()
    checkinResult.value = result
    // Update status to reflect checked-in state
    if (status.value) {
      status.value.checked_in_today = true
      status.value.reward_amount = result.reward_amount
      status.value.checkin_at = result.checkin_at
    }
    // Refresh user balance
    await authStore.refreshUser().catch(() => {})
    // Show success toast
    appStore.showSuccess(t('dashboard.checkin.checkinSuccess', { amount: formatAmount(result.reward_amount) }))
  } catch (err: unknown) {
    const axiosErr = err as { response?: { data?: { message?: string } } }
    const msg = axiosErr?.response?.data?.message
    if (msg?.includes('already') || msg?.includes('DAILY_CHECKIN_ALREADY_DONE')) {
      appStore.showInfo(t('dashboard.checkin.alreadyDone'))
      // Reload status to reflect the actual state
      await loadStatus()
    } else if (msg?.includes('disabled') || msg?.includes('DAILY_CHECKIN_DISABLED')) {
      error.value = t('dashboard.checkin.disabled')
    } else {
      appStore.showError(t('dashboard.checkin.checkinFailed'))
    }
  } finally {
    checking.value = false
  }
}

onMounted(() => {
  if (visible.value) {
    loadStatus()
  }
})
</script>
