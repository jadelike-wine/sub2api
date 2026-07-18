<template>
  <section class="rounded-2xl border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800">
    <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
      {{ t('admin.imageCredentials.cleanup.title') }}
    </h3>
    <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
      {{ t('admin.imageCredentials.cleanup.description') }}
    </p>

    <div class="mt-4 space-y-4">
      <!-- Filter Mode Selector -->
      <div class="flex flex-wrap items-center gap-3">
        <span class="text-xs text-gray-500 dark:text-dark-400">
          {{ t('admin.imageCredentials.cleanup.filterMode') }}:
        </span>
        <div class="flex gap-2">
          <button
            type="button"
            class="rounded-lg px-3 py-1.5 text-xs font-medium transition"
            :class="filterMode === 'days'
              ? 'bg-primary-600 text-white'
              : 'border border-gray-300 bg-white text-gray-700 hover:bg-gray-50 dark:border-dark-600 dark:bg-dark-800 dark:text-dark-200 dark:hover:bg-dark-700'"
            @click="onModeChange('days')"
          >
            {{ t('admin.imageCredentials.cleanup.modeOlderThan') }}
          </button>
          <button
            type="button"
            class="rounded-lg px-3 py-1.5 text-xs font-medium transition"
            :class="filterMode === 'date'
              ? 'bg-primary-600 text-white'
              : 'border border-gray-300 bg-white text-gray-700 hover:bg-gray-50 dark:border-dark-600 dark:bg-dark-800 dark:text-dark-200 dark:hover:bg-dark-700'"
            @click="onModeChange('date')"
          >
            {{ t('admin.imageCredentials.cleanup.modeBeforeDate') }}
          </button>
        </div>
      </div>

      <!-- Filter Inputs -->
      <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div v-if="filterMode === 'days'">
          <label class="block text-xs font-medium text-gray-700 dark:text-dark-200">
            {{ t('admin.imageCredentials.cleanup.olderThanDays') }}
          </label>
          <input
            v-model.number="filterDays"
            type="number"
            min="0"
            :placeholder="t('admin.imageCredentials.cleanup.olderThanDaysPlaceholder')"
            class="mt-1 w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 focus:border-primary-500 focus:outline-none dark:border-dark-600 dark:bg-dark-900 dark:text-white"
          />
        </div>
        <div v-if="filterMode === 'date'">
          <label class="block text-xs font-medium text-gray-700 dark:text-dark-200">
            {{ t('admin.imageCredentials.cleanup.beforeDate') }}
          </label>
          <input
            v-model="filterDate"
            type="date"
            :placeholder="t('admin.imageCredentials.cleanup.beforeDatePlaceholder')"
            class="mt-1 w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 focus:border-primary-500 focus:outline-none dark:border-dark-600 dark:bg-dark-900 dark:text-white"
          />
        </div>
        <div>
          <label class="block text-xs font-medium text-gray-700 dark:text-dark-200">
            {{ t('admin.imageCredentials.cleanup.batchSize') }}
          </label>
          <input
            v-model.number="batchSize"
            type="number"
            min="0"
            max="5000"
            :placeholder="t('admin.imageCredentials.cleanup.batchSizePlaceholder')"
            class="mt-1 w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 focus:border-primary-500 focus:outline-none dark:border-dark-600 dark:bg-dark-900 dark:text-white"
          />
          <p class="mt-1 text-xs text-gray-400 dark:text-dark-500">
            {{ t('admin.imageCredentials.cleanup.batchSizeHint') }}
          </p>
        </div>
      </div>

      <!-- Validation Error -->
      <p v-if="validationError" class="text-xs text-red-600 dark:text-red-400">
        {{ validationError }}
      </p>

      <!-- Preview Result -->
      <div v-if="previewError" class="rounded-lg bg-red-50 p-3 text-xs text-red-700 dark:bg-red-900/20 dark:text-red-300">
        {{ t('admin.imageCredentials.cleanup.previewError') }}: {{ previewError }}
      </div>
      <div
        v-else-if="previewResult !== null"
        class="rounded-lg p-3 text-xs"
        :class="previewResult.count > 0
          ? 'bg-amber-50 text-amber-800 dark:bg-amber-900/20 dark:text-amber-300'
          : 'bg-green-50 text-green-800 dark:bg-green-900/20 dark:text-green-300'"
      >
        <template v-if="previewResult.count > 0">
          {{ t('admin.imageCredentials.cleanup.previewResult', {
            count: previewResult.count,
            cutoff: formatTime(previewResult.cutoff)
          }) }}
        </template>
        <template v-else>
          {{ t('admin.imageCredentials.cleanup.previewEmpty') }}
        </template>
      </div>

      <!-- Action Buttons -->
      <div class="flex flex-wrap gap-2">
        <button
          type="button"
          class="rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:cursor-not-allowed disabled:opacity-50 dark:border-dark-600 dark:bg-dark-800 dark:text-dark-200 dark:hover:bg-dark-700"
          :disabled="previewing || executing || !!validationError"
          @click="onPreview"
        >
          {{ previewing ? t('admin.imageCredentials.cleanup.previewing') : t('admin.imageCredentials.cleanup.preview') }}
        </button>
        <button
          type="button"
          class="rounded-lg bg-red-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-red-700 disabled:cursor-not-allowed disabled:opacity-50"
          :disabled="previewing || executing || !!validationError"
          @click="onExecute"
        >
          {{ executing ? t('admin.imageCredentials.cleanup.executing') : t('admin.imageCredentials.cleanup.execute') }}
        </button>
      </div>
    </div>

    <!-- Confirm Dialog -->
    <BaseDialog
      :show="confirmModal.show"
      :title="t('admin.imageCredentials.cleanup.confirmTitle')"
      width="narrow"
      @close="confirmModal.show = false"
    >
      <div class="space-y-3">
        <p class="text-sm text-gray-700 dark:text-dark-200">
          {{ t('admin.imageCredentials.cleanup.confirmMessage', {
            count: confirmModal.count
          }) }}
        </p>
        <p v-if="confirmModal.error" class="text-xs text-red-600 dark:text-red-400">
          {{ confirmModal.error }}
        </p>
      </div>
      <template #footer>
        <div class="flex justify-end gap-2">
          <button
            type="button"
            class="rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 dark:border-dark-600 dark:bg-dark-800 dark:text-dark-200 dark:hover:bg-dark-700"
            :disabled="executing"
            @click="confirmModal.show = false"
          >
            {{ t('admin.imageCredentials.cleanup.cancel') }}
          </button>
          <button
            type="button"
            class="rounded-lg bg-red-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-red-700 disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="executing"
            @click="confirmExecute"
          >
            {{ executing ? t('admin.imageCredentials.cleanup.executing') : t('admin.imageCredentials.cleanup.confirm') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- Result Dialog -->
    <BaseDialog
      :show="resultModal.show"
      :title="t('admin.imageCredentials.cleanup.result.title')"
      width="narrow"
      @close="resultModal.show = false"
    >
      <div v-if="resultModal.result" class="space-y-3">
        <div
          class="rounded-lg p-3 text-sm font-medium"
          :class="resultModal.result.storage_failures > 0 || resultModal.result.db_failures > 0
            ? 'bg-amber-50 text-amber-800 dark:bg-amber-900/20 dark:text-amber-300'
            : 'bg-green-50 text-green-800 dark:bg-green-900/20 dark:text-green-300'"
        >
          {{ resultModal.result.storage_failures > 0 || resultModal.result.db_failures > 0
            ? t('admin.imageCredentials.cleanup.result.partial')
            : t('admin.imageCredentials.cleanup.result.success') }}
        </div>
        <dl class="grid grid-cols-2 gap-x-4 gap-y-2 text-xs">
          <dt class="text-gray-500 dark:text-dark-400">
            {{ t('admin.imageCredentials.cleanup.result.scanned') }}
          </dt>
          <dd class="text-gray-900 dark:text-white">{{ resultModal.result.scanned }}</dd>
          <dt class="text-gray-500 dark:text-dark-400">
            {{ t('admin.imageCredentials.cleanup.result.deletedAssets') }}
          </dt>
          <dd class="text-gray-900 dark:text-white">{{ resultModal.result.deleted_assets }}</dd>
          <dt class="text-gray-500 dark:text-dark-400">
            {{ t('admin.imageCredentials.cleanup.result.deletedStorageObjects') }}
          </dt>
          <dd class="text-gray-900 dark:text-white">{{ resultModal.result.deleted_storage_objects }}</dd>
          <dt class="text-gray-500 dark:text-dark-400">
            {{ t('admin.imageCredentials.cleanup.result.storageFailures') }}
          </dt>
          <dd :class="resultModal.result.storage_failures > 0 ? 'text-amber-700 dark:text-amber-400' : 'text-gray-900 dark:text-white'">
            {{ resultModal.result.storage_failures }}
          </dd>
          <dt class="text-gray-500 dark:text-dark-400">
            {{ t('admin.imageCredentials.cleanup.result.dbFailures') }}
          </dt>
          <dd :class="resultModal.result.db_failures > 0 ? 'text-amber-700 dark:text-amber-400' : 'text-gray-900 dark:text-white'">
            {{ resultModal.result.db_failures }}
          </dd>
          <dt class="text-gray-500 dark:text-dark-400">
            {{ t('admin.imageCredentials.cleanup.result.durationMs') }}
          </dt>
          <dd class="text-gray-900 dark:text-white">{{ resultModal.result.duration_ms }}</dd>
          <dt class="text-gray-500 dark:text-dark-400">
            {{ t('admin.imageCredentials.cleanup.result.cutoff') }}
          </dt>
          <dd class="text-gray-900 dark:text-white">{{ formatTime(resultModal.result.cutoff) }}</dd>
        </dl>
      </div>
      <template #footer>
        <div class="flex justify-end">
          <button
            type="button"
            class="rounded-lg bg-primary-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-primary-700"
            @click="resultModal.show = false"
          >
            {{ t('admin.imageCredentials.cleanup.result.close') }}
          </button>
        </div>
      </template>
    </BaseDialog>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import adminImageGenerationAPI from '@/api/admin/imageGeneration'
import type {
  ImageAssetCleanupPreview,
  ImageAssetCleanupRequest,
  ImageAssetCleanupResult,
} from '@/types'

const { t } = useI18n()

// ==================== State ====================

type FilterMode = 'days' | 'date'
const filterMode = ref<FilterMode>('days')
const filterDays = ref<number | null>(null)
const filterDate = ref<string>('')
const batchSize = ref<number | null>(null)

const previewing = ref(false)
const executing = ref(false)
const previewResult = ref<ImageAssetCleanupPreview | null>(null)
const previewError = ref<string>('')

interface ConfirmModal {
  show: boolean
  count: number
  error: string
}
const confirmModal = ref<ConfirmModal>({
  show: false,
  count: 0,
  error: '',
})

interface ResultModal {
  show: boolean
  result: ImageAssetCleanupResult | null
}
const resultModal = ref<ResultModal>({
  show: false,
  result: null,
})

// ==================== Computed ====================

/**
 * 校验输入参数，返回本地化的错误信息（空字符串表示通过）。
 */
const validationError = computed<string>(() => {
  if (filterMode.value === 'days') {
    if (filterDays.value === null || filterDays.value === undefined) return ''
    if (filterDays.value < 0) {
      return t('admin.imageCredentials.cleanup.errors.invalidDays')
    }
  } else if (filterMode.value === 'date') {
    if (!filterDate.value) return ''
    const d = new Date(filterDate.value)
    if (Number.isNaN(d.getTime())) {
      return t('admin.imageCredentials.cleanup.errors.invalidBeforeDate')
    }
    if (d.getTime() > Date.now()) {
      return t('admin.imageCredentials.cleanup.errors.futureDate')
    }
  }
  return ''
})

/**
 * 构造清理请求参数。
 */
function buildRequest(): ImageAssetCleanupRequest {
  const req: ImageAssetCleanupRequest = {}
  if (filterMode.value === 'days' && filterDays.value !== null && filterDays.value !== undefined) {
    req.older_than_days = filterDays.value
  } else if (filterMode.value === 'date' && filterDate.value) {
    // 后端要求 RFC3339，前端 date input 是 yyyy-mm-dd，补齐为当天结束时刻
    const d = new Date(filterDate.value)
    if (!Number.isNaN(d.getTime())) {
      // 设置为当天 23:59:59 本地时间，转 ISO 后取前 19 位 + 'Z'
      d.setHours(23, 59, 59, 0)
      req.before_date = d.toISOString()
    }
  }
  if (batchSize.value !== null && batchSize.value !== undefined && batchSize.value > 0) {
    req.batch_size = batchSize.value
  }
  return req
}

// ==================== Helpers ====================

function formatTime(iso: string | null | undefined): string {
  if (!iso) return '—'
  try {
    const d = new Date(iso)
    if (Number.isNaN(d.getTime())) return iso
    return d.toLocaleString()
  } catch {
    return iso
  }
}

function onModeChange(mode: FilterMode) {
  if (filterMode.value === mode) return
  filterMode.value = mode
  // 切换模式时清空预览，避免显示过期的统计
  previewResult.value = null
  previewError.value = ''
}

// ==================== Actions ====================

async function onPreview() {
  if (validationError.value) return
  previewing.value = true
  previewError.value = ''
  previewResult.value = null
  try {
    const result = await adminImageGenerationAPI.previewAssetCleanup(buildRequest())
    previewResult.value = result
  } catch (err) {
    previewError.value = extractErrorMessage(err)
  } finally {
    previewing.value = false
  }
}

function onExecute() {
  if (validationError.value) return
  // 没有预览过或预览失败时，强制要求先预览以避免误操作
  if (previewResult.value === null) {
    confirmModal.value = {
      show: true,
      count: 0,
      error: t('admin.imageCredentials.cleanup.previewError'),
    }
    return
  }
  confirmModal.value = {
    show: true,
    count: previewResult.value.count,
    error: '',
  }
}

async function confirmExecute() {
  executing.value = true
  confirmModal.value.error = ''
  try {
    const result = await adminImageGenerationAPI.cleanupAssets(buildRequest())
    resultModal.value = {
      show: true,
      result,
    }
    confirmModal.value.show = false
    // 清理完成后清除预览（数据已变）
    previewResult.value = null
  } catch (err) {
    confirmModal.value.error = extractErrorMessage(err)
  } finally {
    executing.value = false
  }
}

/**
 * 从 axios 错误对象中提取后端返回的 message（已脱敏）。
 * 后端响应格式：{ code, message, reason?, data? }
 */
function extractErrorMessage(err: unknown): string {
  if (!err) return t('admin.imageCredentials.cleanup.errors.execute')
  // axios error 的 response.data 包含后端 Response 结构
  const e = err as { response?: { data?: { message?: string; reason?: string } }; message?: string }
  if (e.response?.data?.message) {
    return e.response.data.reason
      ? `${e.response.data.message} (${e.response.data.reason})`
      : e.response.data.message
  }
  if (e.message) return e.message
  return t('admin.imageCredentials.cleanup.errors.execute')
}
</script>
