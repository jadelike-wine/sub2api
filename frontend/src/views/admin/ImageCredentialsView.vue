<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl">
      <!-- Page Header -->
      <div class="mb-6 flex items-start justify-between">
        <div>
          <h2 class="text-xl font-semibold text-gray-900 dark:text-white">
            {{ t('admin.imageCredentials.title') }}
          </h2>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">
            {{ t('admin.imageCredentials.description') }}
          </p>
        </div>
        <div class="flex gap-2">
          <button
            type="button"
            class="rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 dark:border-dark-600 dark:bg-dark-800 dark:text-dark-200 dark:hover:bg-dark-700"
            :disabled="loading"
            @click="loadAll"
          >
            {{ t('admin.imageCredentials.actions.refresh') }}
          </button>
          <button
            type="button"
            class="rounded-lg bg-primary-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-primary-700 disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="loading"
            @click="openCreateModal"
          >
            {{ t('admin.imageCredentials.actions.create') }}
          </button>
        </div>
      </div>

      <!-- Storage Status Card -->
      <section class="mb-6 rounded-2xl border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800">
        <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
          {{ t('admin.imageCredentials.storage.title') }}
        </h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
          {{ t('admin.imageCredentials.storage.description') }}
        </p>
        <div class="mt-3 flex flex-wrap items-center gap-4">
          <div class="flex items-center gap-2">
            <span class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.imageCredentials.storage.driver') }}:</span>
            <span class="inline-flex items-center gap-1 rounded-full bg-blue-100 px-2 py-0.5 text-xs font-medium text-blue-700 dark:bg-blue-900/30 dark:text-blue-300">
              {{ storageStatus?.driver === 'local' ? t('admin.imageCredentials.storage.driverLocal') : t('admin.imageCredentials.storage.driverS3') }}
            </span>
          </div>
          <div class="flex items-center gap-2">
            <span class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.imageCredentials.storage.configured') }}:</span>
            <span
              v-if="storageStatus?.configured"
              class="inline-flex items-center gap-1 rounded-full bg-green-100 px-2 py-0.5 text-xs font-medium text-green-700 dark:bg-green-900/30 dark:text-green-300"
            >
              <span class="h-1.5 w-1.5 rounded-full bg-green-500" />
              {{ t('admin.imageCredentials.storage.configured') }}
            </span>
            <span
              v-else
              class="inline-flex items-center gap-1 rounded-full bg-red-100 px-2 py-0.5 text-xs font-medium text-red-700 dark:bg-red-900/30 dark:text-red-300"
            >
              <span class="h-1.5 w-1.5 rounded-full bg-red-500" />
              {{ t('admin.imageCredentials.storage.notConfigured') }}
            </span>
          </div>
          <div class="flex items-center gap-2">
            <span class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.imageCredentials.storage.bucket') }}:</span>
            <span class="text-sm font-medium text-gray-900 dark:text-white">
              {{ storageStatus?.bucket || t('admin.imageCredentials.storage.notAvailable') }}
            </span>
          </div>
        </div>
        <p v-if="!storageStatus?.configured" class="mt-3 text-xs text-gray-400 dark:text-dark-500">
          {{ t('admin.imageCredentials.storage.hint') }}
        </p>
      </section>

      <!-- Asset Cleanup Panel -->
      <AssetCleanupPanel class="mb-6" />

      <!-- Image Pricing Card -->
      <section class="mb-6 rounded-2xl border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800">
        <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
          {{ t('admin.imageCredentials.imagePricing.title') }}
        </h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
          {{ t('admin.imageCredentials.imagePricing.description') }}
        </p>
        <div v-if="pricingLoading" class="mt-4 text-sm text-gray-500 dark:text-dark-400">
          {{ t('common.loading') }}
        </div>
        <div v-else class="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <div v-for="tier in pricingTiers" :key="tier.key">
            <label class="block text-xs font-medium text-gray-700 dark:text-dark-200">
              {{ t(tier.labelKey) }}
              <span class="ml-1 text-gray-400 dark:text-dark-500">({{ t('admin.imageCredentials.imagePricing.tierUnit') }})</span>
            </label>
            <input
              v-model.number="pricingForm[tier.key]"
              type="number"
              step="0.001"
              min="0"
              :placeholder="t('admin.imageCredentials.imagePricing.placeholder')"
              class="mt-1 block w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 focus:border-primary-500 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-900 dark:text-white"
            />
            <p class="mt-1 text-xs text-gray-400 dark:text-dark-500">
              <span v-if="pricingOriginal[tier.key] === null || pricingOriginal[tier.key] === undefined">
                {{ t('admin.imageCredentials.imagePricing.notConfigured') }}
              </span>
              <span v-else>
                {{ t('admin.imageCredentials.imagePricing.preview', { tier: tier.label, price: (pricingForm[tier.key] ?? pricingOriginal[tier.key] ?? 0).toFixed(6) }) }}
              </span>
            </p>
          </div>
        </div>
        <div class="mt-4 flex items-center gap-3">
          <button
            type="button"
            class="rounded-lg bg-primary-600 px-4 py-2 text-sm font-medium text-white hover:bg-primary-700 disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="pricingSaving"
            @click="onSavePricing"
          >
            {{ pricingSaving ? t('admin.imageCredentials.imagePricing.saving') : t('admin.imageCredentials.imagePricing.save') }}
          </button>
          <p class="text-xs text-gray-500 dark:text-dark-400">
            {{ t('admin.imageCredentials.imagePricing.hint') }}
          </p>
        </div>
      </section>

      <!-- Generation Concurrency Config Card -->
      <section class="mb-6 rounded-2xl border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800">
        <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
          {{ t('admin.imageCredentials.generationConfig.title') }}
        </h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
          {{ t('admin.imageCredentials.generationConfig.description') }}
        </p>
        <div v-if="genConfigLoading" class="mt-4 text-sm text-gray-500 dark:text-dark-400">
          {{ t('common.loading') }}
        </div>
        <div v-else class="mt-4">
          <div class="flex items-end gap-3">
            <div class="w-48">
              <label class="block text-xs font-medium text-gray-700 dark:text-dark-200">
                {{ t('admin.imageCredentials.generationConfig.maxConcurrentPerUser') }}
              </label>
              <input
                v-model.number="genConfigForm.maxConcurrentPerUser"
                type="number"
                min="1"
                step="1"
                :placeholder="t('admin.imageCredentials.generationConfig.placeholder')"
                class="mt-1 block w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 focus:border-primary-500 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-900 dark:text-white"
              />
            </div>
            <button
              type="button"
              class="rounded-lg bg-primary-600 px-4 py-2 text-sm font-medium text-white hover:bg-primary-700 disabled:cursor-not-allowed disabled:opacity-50"
              :disabled="genConfigSaving"
              @click="onSaveGenerationConfig"
            >
              {{ genConfigSaving ? t('admin.imageCredentials.generationConfig.saving') : t('admin.imageCredentials.generationConfig.save') }}
            </button>
          </div>
          <p class="mt-2 text-xs text-gray-400 dark:text-dark-500">
            <span v-if="genConfigOriginal.configured">
              {{ t('admin.imageCredentials.generationConfig.currentValue', { value: genConfigOriginal.max_concurrent_per_user }) }}
            </span>
            <span v-else>
              {{ t('admin.imageCredentials.generationConfig.notConfigured', { default: genConfigOriginal.config_default }) }}
            </span>
          </p>
          <p class="mt-1 text-xs text-gray-400 dark:text-dark-500">
            {{ t('admin.imageCredentials.generationConfig.hint') }}
          </p>
        </div>
      </section>

      <!-- Credentials Table -->
      <section class="rounded-2xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
        <div v-if="loading && credentials.length === 0" class="p-8 text-center text-sm text-gray-500 dark:text-dark-400">
          {{ t('common.loading') }}
        </div>
        <div v-else-if="credentials.length === 0" class="p-8 text-center text-sm text-gray-500 dark:text-dark-400">
          {{ t('admin.imageCredentials.empty') }}
        </div>
        <div v-else class="overflow-x-auto">
          <table class="w-full text-left text-sm">
            <thead class="border-b border-gray-200 text-xs uppercase text-gray-500 dark:border-dark-700 dark:text-dark-400">
              <tr>
                <th class="px-4 py-3 font-medium">{{ t('admin.imageCredentials.columns.name') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.imageCredentials.columns.fingerprint') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.imageCredentials.columns.status') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.imageCredentials.columns.priority') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.imageCredentials.columns.weight') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.imageCredentials.columns.enabled') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.imageCredentials.columns.lastUsedAt') }}</th>
                <th class="px-4 py-3 font-medium">{{ t('admin.imageCredentials.columns.actions') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="cred in credentials" :key="cred.id" class="hover:bg-gray-50 dark:hover:bg-dark-700/50">
                <td class="px-4 py-3">
                  <div class="font-medium text-gray-900 dark:text-white">{{ cred.name }}</div>
                  <div class="text-xs text-gray-500 dark:text-dark-400">{{ cred.provider }}</div>
                </td>
                <td class="px-4 py-3 font-mono text-xs text-gray-600 dark:text-dark-300">{{ cred.key_fingerprint }}</td>
                <td class="px-4 py-3">
                  <span
                    class="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium"
                    :class="statusBadgeClass(cred)"
                  >
                    <span class="h-1.5 w-1.5 rounded-full" :class="statusDotClass(cred)" />
                    {{ t(`admin.imageCredentials.status.${cred.status}`) }}
                  </span>
                  <div v-if="cred.consecutive_failures > 0" class="mt-1 text-xs text-red-500">
                    {{ cred.consecutive_failures }} / {{ cred.last_error_code || '—' }}
                  </div>
                </td>
                <td class="px-4 py-3 text-gray-900 dark:text-white">{{ cred.priority }}</td>
                <td class="px-4 py-3 text-gray-900 dark:text-white">{{ cred.weight }}</td>
                <td class="px-4 py-3">
                  <span
                    class="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium"
                    :class="cred.enabled
                      ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
                      : 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-dark-400'"
                  >
                    {{ cred.enabled ? '✓' : '×' }}
                  </span>
                </td>
                <td class="px-4 py-3 text-xs text-gray-500 dark:text-dark-400">
                  {{ formatTime(cred.last_used_at) }}
                  <div v-if="cred.cooldown_until" class="mt-0.5 text-orange-500">
                    ⏱ {{ formatTime(cred.cooldown_until) }}
                  </div>
                </td>
                <td class="px-4 py-3">
                  <div class="flex gap-2">
                    <button
                      type="button"
                      class="text-xs text-primary-600 hover:underline dark:text-primary-400"
                      @click="openEditModal(cred)"
                    >
                      {{ t('admin.imageCredentials.actions.edit') }}
                    </button>
                    <button
                      type="button"
                      class="text-xs text-blue-600 hover:underline dark:text-blue-400"
                      :disabled="testingId === cred.id"
                      @click="onTestCredential(cred.id)"
                    >
                      {{ testingId === cred.id ? t('admin.imageCredentials.test.running') : t('admin.imageCredentials.actions.test') }}
                    </button>
                    <button
                      type="button"
                      class="text-xs text-red-600 hover:underline dark:text-red-400"
                      @click="onDeleteCredential(cred)"
                    >
                      {{ t('admin.imageCredentials.actions.delete') }}
                    </button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>

    <!-- Create / Edit Modal -->
    <BaseDialog
      :show="formModal.show"
      :title="formModal.mode === 'create'
        ? t('admin.imageCredentials.form.createTitle')
        : t('admin.imageCredentials.form.editTitle')"
      width="normal"
      @close="closeFormModal"
    >
      <form @submit.prevent="onSubmitForm" class="space-y-4">
        <!-- Name -->
        <div>
          <label class="block text-sm font-medium text-gray-700 dark:text-dark-200">
            {{ t('admin.imageCredentials.form.name') }}
          </label>
          <input
            v-model="form.name"
            type="text"
            :placeholder="t('admin.imageCredentials.form.namePlaceholder')"
            class="mt-1 w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 focus:border-primary-500 focus:outline-none dark:border-dark-600 dark:bg-dark-900 dark:text-white"
          />
          <p v-if="formErrors.name" class="mt-1 text-xs text-red-600 dark:text-red-400">{{ formErrors.name }}</p>
        </div>

        <!-- Provider (only on create) -->
        <div v-if="formModal.mode === 'create'">
          <label class="block text-sm font-medium text-gray-700 dark:text-dark-200">
            {{ t('admin.imageCredentials.form.provider') }}
          </label>
          <input
            v-model="form.provider"
            type="text"
            placeholder="agnes"
            class="mt-1 w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 focus:border-primary-500 focus:outline-none dark:border-dark-600 dark:bg-dark-900 dark:text-white"
          />
          <p class="mt-1 text-xs text-gray-400 dark:text-dark-500">{{ t('admin.imageCredentials.form.providerHint') }}</p>
        </div>

        <!-- API Key -->
        <div>
          <label class="block text-sm font-medium text-gray-700 dark:text-dark-200">
            {{ t('admin.imageCredentials.form.apiKey') }}
          </label>
          <input
            v-model="form.apiKey"
            type="password"
            autocomplete="off"
            :placeholder="formModal.mode === 'create'
              ? t('admin.imageCredentials.form.apiKeyPlaceholder')
              : '••••••••'"
            class="mt-1 w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 focus:border-primary-500 focus:outline-none dark:border-dark-600 dark:bg-dark-900 dark:text-white"
          />
          <p class="mt-1 text-xs text-gray-400 dark:text-dark-500">
            {{ formModal.mode === 'create'
              ? t('admin.imageCredentials.form.apiKeyHint')
              : t('admin.imageCredentials.form.apiKeyKeepHint') }}
          </p>
          <p v-if="formErrors.apiKey" class="mt-1 text-xs text-red-600 dark:text-red-400">{{ formErrors.apiKey }}</p>
        </div>

        <!-- Enabled -->
        <div class="flex items-center gap-2">
          <input
            v-model="form.enabled"
            type="checkbox"
            id="cred-enabled"
            class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
          />
          <label for="cred-enabled" class="text-sm text-gray-700 dark:text-dark-200">
            {{ t('admin.imageCredentials.form.enabled') }}
          </label>
        </div>

        <!-- Priority + Weight -->
        <div class="grid grid-cols-2 gap-4">
          <div>
            <label class="block text-sm font-medium text-gray-700 dark:text-dark-200">
              {{ t('admin.imageCredentials.form.priority') }}
            </label>
            <input
              v-model.number="form.priority"
              type="number"
              min="0"
              class="mt-1 w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 focus:border-primary-500 focus:outline-none dark:border-dark-600 dark:bg-dark-900 dark:text-white"
            />
            <p class="mt-1 text-xs text-gray-400 dark:text-dark-500">{{ t('admin.imageCredentials.form.priorityHint') }}</p>
          </div>
          <div>
            <label class="block text-sm font-medium text-gray-700 dark:text-dark-200">
              {{ t('admin.imageCredentials.form.weight') }}
            </label>
            <input
              v-model.number="form.weight"
              type="number"
              min="0"
              class="mt-1 w-full rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 focus:border-primary-500 focus:outline-none dark:border-dark-600 dark:bg-dark-900 dark:text-white"
            />
            <p class="mt-1 text-xs text-gray-400 dark:text-dark-500">{{ t('admin.imageCredentials.form.weightHint') }}</p>
          </div>
        </div>

        <p v-if="formErrors.save" class="text-xs text-red-600 dark:text-red-400">{{ formErrors.save }}</p>
      </form>

      <template #footer>
        <div class="flex justify-end gap-2">
          <button
            type="button"
            class="rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 dark:border-dark-600 dark:bg-dark-800 dark:text-dark-200 dark:hover:bg-dark-700"
            @click="closeFormModal"
          >
            {{ t('admin.imageCredentials.actions.cancel') }}
          </button>
          <button
            type="button"
            class="rounded-lg bg-primary-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-primary-700 disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="submitting"
            @click="onSubmitForm"
          >
            {{ submitting ? t('common.saving') : t('admin.imageCredentials.actions.save') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- Test Result Modal -->
    <BaseDialog
      :show="testResult !== null"
      :title="t('admin.imageCredentials.test.title')"
      width="narrow"
      @close="testResult = null"
    >
      <div v-if="testResult" class="space-y-3">
        <div
          class="rounded-lg p-3 text-sm"
          :class="testResult.success
            ? 'bg-green-50 text-green-700 dark:bg-green-900/20 dark:text-green-300'
            : 'bg-red-50 text-red-700 dark:bg-red-900/20 dark:text-red-300'"
        >
          <p class="font-medium">
            {{ testResult.success
              ? t('admin.imageCredentials.test.success')
              : t('admin.imageCredentials.test.failure') }}
          </p>
        </div>

        <dl class="grid grid-cols-2 gap-2 text-sm">
          <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.imageCredentials.test.httpStatus') }}</dt>
          <dd class="text-gray-900 dark:text-white">{{ testResult.http_status }}</dd>

          <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.imageCredentials.test.duration') }}</dt>
          <dd class="text-gray-900 dark:text-white">{{ testResult.duration_ms }} ms</dd>

          <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.imageCredentials.test.fingerprint') }}</dt>
          <dd class="font-mono text-xs text-gray-900 dark:text-white">{{ testResult.key_fingerprint }}</dd>

          <template v-if="!testResult.success">
            <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.imageCredentials.test.errorCode') }}</dt>
            <dd class="text-gray-900 dark:text-white">{{ testResult.error_code || '—' }}</dd>

            <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.imageCredentials.test.errorMessage') }}</dt>
            <dd class="col-span-1 text-xs text-gray-900 dark:text-white">{{ testResult.error_message || '—' }}</dd>
          </template>
        </dl>
      </div>

      <template #footer>
        <div class="flex justify-end">
          <button
            type="button"
            class="rounded-lg bg-primary-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-primary-700"
            @click="testResult = null"
          >
            {{ t('admin.imageCredentials.test.close') }}
          </button>
        </div>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import AssetCleanupPanel from '@/components/admin/image/AssetCleanupPanel.vue'
import adminImageGenerationAPI from '@/api/admin/imageGeneration'
import type {
  CreateImageCredentialRequest,
  ImageCredentialStatus,
  ImageGenerationConfig,
  ImagePriceConfig,
  ImageProviderCredential,
  ImageStorageStatus,
  TestImageCredentialResult,
  UpdateImageCredentialRequest,
} from '@/types'

const { t } = useI18n()

// ==================== State ====================

const loading = ref(false)
const submitting = ref(false)
const testingId = ref<number | null>(null)
const credentials = ref<ImageProviderCredential[]>([])
const storageStatus = ref<ImageStorageStatus | null>(null)
const testResult = ref<TestImageCredentialResult | null>(null)

// AI 生图分层价格配置
// pricingOriginal: 后端返回的原始配置（null 表示该 tier 未配置）
// pricingForm: 表单输入值；用户清空输入框时为 null，由 onSavePricing 序列化为 null 提交
type PricingTierKey = 'price_1k_usd' | 'price_2k_usd' | 'price_3k_usd' | 'price_4k_usd'
const pricingTiers: { key: PricingTierKey; label: string; labelKey: string }[] = [
  { key: 'price_1k_usd', label: '1K', labelKey: 'admin.imageCredentials.imagePricing.tier1K' },
  { key: 'price_2k_usd', label: '2K', labelKey: 'admin.imageCredentials.imagePricing.tier2K' },
  { key: 'price_3k_usd', label: '3K', labelKey: 'admin.imageCredentials.imagePricing.tier3K' },
  { key: 'price_4k_usd', label: '4K', labelKey: 'admin.imageCredentials.imagePricing.tier4K' },
]
const pricingLoading = ref(false)
const pricingSaving = ref(false)
const pricingOriginal = ref<ImagePriceConfig>({
  price_1k_usd: null,
  price_2k_usd: null,
  price_3k_usd: null,
  price_4k_usd: null,
})
const pricingForm = ref<ImagePriceConfig>({
  price_1k_usd: null,
  price_2k_usd: null,
  price_3k_usd: null,
  price_4k_usd: null,
})

// AI 生图并发配置
const genConfigLoading = ref(false)
const genConfigSaving = ref(false)
const genConfigOriginal = ref<ImageGenerationConfig>({
  max_concurrent_per_user: 0,
  config_default: 0,
  configured: false,
})
const genConfigForm = ref<{ maxConcurrentPerUser: number | string | null }>({
  maxConcurrentPerUser: null,
})

interface FormState {
  name: string
  provider: string
  apiKey: string
  enabled: boolean
  // 使用 null 表示"未指定"，让后端使用默认值；0 是合法的优先级值
  priority: number | null
  weight: number | null
}

interface FormErrors {
  name?: string
  apiKey?: string
  save?: string
}

const formModal = ref<{ show: boolean; mode: 'create' | 'edit'; editingId?: number }>({
  show: false,
  mode: 'create',
})
const form = ref<FormState>({
  name: '',
  provider: 'agnes',
  apiKey: '',
  enabled: true,
  priority: null,
  weight: null,
})
const formErrors = ref<FormErrors>({})

// ==================== Helpers ====================

function formatTime(iso: string | null): string {
  if (!iso) return '—'
  try {
    const d = new Date(iso)
    if (Number.isNaN(d.getTime())) return iso
    return d.toLocaleString()
  } catch {
    return iso
  }
}

function statusBadgeClass(cred: ImageProviderCredential): string {
  if (!cred.enabled) {
    return 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-dark-400'
  }
  switch (cred.status) {
    case 'healthy':
      return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
    case 'unhealthy':
      return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
    default:
      return 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-dark-200'
  }
}

function statusDotClass(cred: ImageProviderCredential): string {
  if (!cred.enabled) return 'bg-gray-400'
  switch (cred.status as ImageCredentialStatus) {
    case 'healthy':
      return 'bg-green-500'
    case 'unhealthy':
      return 'bg-red-500'
    default:
      return 'bg-gray-400'
  }
}

function resetForm() {
  form.value = {
    name: '',
    provider: 'agnes',
    apiKey: '',
    enabled: true,
    priority: null,
    weight: null,
  }
  formErrors.value = {}
}

// ==================== Data Loading ====================

async function loadCredentials() {
  try {
    credentials.value = await adminImageGenerationAPI.listCredentials()
  } catch (err) {
    console.error(t('admin.imageCredentials.messages.loadError'), err)
  }
}

async function loadStorageStatus() {
  try {
    storageStatus.value = await adminImageGenerationAPI.getStorageStatus()
  } catch (err) {
    console.error(t('admin.imageCredentials.messages.storageError'), err)
  }
}

async function loadAll() {
  loading.value = true
  await Promise.all([loadCredentials(), loadStorageStatus(), loadPricing(), loadGenerationConfig()])
  loading.value = false
}

// ==================== Image Pricing ====================

async function loadPricing() {
  pricingLoading.value = true
  try {
    const cfg = await adminImageGenerationAPI.getImagePricing()
    pricingOriginal.value = { ...cfg }
    pricingForm.value = { ...cfg }
  } catch (err) {
    console.error(t('admin.imageCredentials.imagePricing.loadError'), err)
  } finally {
    pricingLoading.value = false
  }
}

async function onSavePricing() {
  // 归一化表单值：v-model.number 清空输入框会得到空字符串 ''，
  // 必须转成 null 才能匹配后端 *float64 的 JSON 反序列化（"" 会触发 400）。
  // null 表示"不修改该 tier"（patch 语义），后端保留原值。
  const normalize = (v: number | string | null | undefined): number | null => {
    if (v === null || v === undefined || v === '') return null
    const n = typeof v === 'number' ? v : Number(v)
    return Number.isNaN(n) ? null : n
  }
  const normalized: ImagePriceConfig = {
    price_1k_usd: normalize(pricingForm.value.price_1k_usd),
    price_2k_usd: normalize(pricingForm.value.price_2k_usd),
    price_3k_usd: normalize(pricingForm.value.price_3k_usd),
    price_4k_usd: normalize(pricingForm.value.price_4k_usd),
  }
  // 客户端校验：非 null 值必须 >= 0
  for (const tier of pricingTiers) {
    const v = normalized[tier.key]
    if (v !== null && v < 0) {
      ElMessage.error(t('admin.imageCredentials.imagePricing.invalidPrice'))
      return
    }
  }
  // 后端要求至少配置一个 tier，全空提交会被 400 拒绝；
  // 全空表示用户想"全部使用默认值"，此时无需保存，直接提示。
  const hasAnyTier = Object.values(normalized).some(v => v !== null)
  if (!hasAnyTier) {
    ElMessage.warning(t('admin.imageCredentials.imagePricing.notConfigured'))
    return
  }
  pricingSaving.value = true
  try {
    const updated = await adminImageGenerationAPI.updateImagePricing(normalized)
    pricingOriginal.value = { ...updated }
    pricingForm.value = { ...updated }
    ElMessage.success(t('admin.imageCredentials.imagePricing.saved'))
  } catch (err: any) {
    // axios 拦截器 reject 的对象含 message 字段（后端 BadRequest 的 message）
    const backendMsg = err?.message
    const prefix = t('admin.imageCredentials.imagePricing.saveError')
    ElMessage.error(backendMsg ? `${prefix}: ${backendMsg}` : prefix)
  } finally {
    pricingSaving.value = false
  }
}

// ==================== Generation Concurrency Config ====================

async function loadGenerationConfig() {
  genConfigLoading.value = true
  try {
    const cfg = await adminImageGenerationAPI.getGenerationConfig()
    genConfigOriginal.value = { ...cfg }
    genConfigForm.value = { maxConcurrentPerUser: cfg.max_concurrent_per_user }
  } catch (err) {
    console.error(t('admin.imageCredentials.generationConfig.loadError'), err)
  } finally {
    genConfigLoading.value = false
  }
}

async function onSaveGenerationConfig() {
  const v = genConfigForm.value.maxConcurrentPerUser
  // 归一化：v-model.number 清空输入框得到 ''，需转成 null
  const n = v === null || v === '' ? null : typeof v === 'number' ? v : Number(v)
  if (n === null || Number.isNaN(n)) {
    ElMessage.error(t('admin.imageCredentials.generationConfig.invalidValue'))
    return
  }
  if (!Number.isInteger(n) || n < 1) {
    ElMessage.error(t('admin.imageCredentials.generationConfig.mustBePositiveInteger'))
    return
  }
  genConfigSaving.value = true
  try {
    const updated = await adminImageGenerationAPI.updateGenerationConfig({
      max_concurrent_per_user: n,
    })
    genConfigOriginal.value = { ...updated }
    genConfigForm.value = { maxConcurrentPerUser: updated.max_concurrent_per_user }
    ElMessage.success(t('admin.imageCredentials.generationConfig.saved'))
  } catch (err: any) {
    const backendMsg = err?.message
    const prefix = t('admin.imageCredentials.generationConfig.saveError')
    ElMessage.error(backendMsg ? `${prefix}: ${backendMsg}` : prefix)
  } finally {
    genConfigSaving.value = false
  }
}

// ==================== Modal / Form ====================

function openCreateModal() {
  resetForm()
  formModal.value = { show: true, mode: 'create' }
}

function openEditModal(cred: ImageProviderCredential) {
  resetForm()
  form.value = {
    name: cred.name,
    provider: cred.provider,
    apiKey: '', // 留空表示不更新
    enabled: cred.enabled,
    priority: cred.priority,
    weight: cred.weight,
  }
  formModal.value = { show: true, mode: 'edit', editingId: cred.id }
}

function closeFormModal() {
  formModal.value.show = false
  resetForm()
}

function validateForm(): boolean {
  const errs: FormErrors = {}
  if (!form.value.name.trim()) {
    errs.name = t('admin.imageCredentials.form.errors.nameRequired')
  }
  // create 模式下 API Key 必填；edit 模式下可留空表示不更新
  if (formModal.value.mode === 'create' && !form.value.apiKey.trim()) {
    errs.apiKey = t('admin.imageCredentials.form.errors.apiKeyRequired')
  }
  formErrors.value = errs
  return Object.keys(errs).length === 0
}

async function onSubmitForm() {
  if (!validateForm()) return
  submitting.value = true
  try {
    if (formModal.value.mode === 'create') {
      const payload: CreateImageCredentialRequest = {
        name: form.value.name.trim(),
        provider: form.value.provider.trim() || 'agnes',
        api_key: form.value.apiKey.trim(),
        enabled: form.value.enabled,
      }
      // priority/weight: null 表示用默认值，0 是合法值
      if (form.value.priority !== null) payload.priority = form.value.priority
      if (form.value.weight !== null) payload.weight = form.value.weight
      const created = await adminImageGenerationAPI.createCredential(payload)
      credentials.value = [created, ...credentials.value]
    } else if (formModal.value.editingId != null) {
      const payload: UpdateImageCredentialRequest = {
        name: form.value.name.trim(),
        enabled: form.value.enabled,
      }
      if (form.value.priority !== null) payload.priority = form.value.priority
      if (form.value.weight !== null) payload.weight = form.value.weight
      // 仅在用户填写了新 Key 时才传 api_key
      if (form.value.apiKey.trim()) {
        payload.api_key = form.value.apiKey.trim()
      }
      const updated = await adminImageGenerationAPI.updateCredential(
        formModal.value.editingId,
        payload
      )
      const idx = credentials.value.findIndex((c) => c.id === updated.id)
      if (idx >= 0) {
        credentials.value[idx] = updated
      }
    }
    closeFormModal()
  } catch (err) {
    formErrors.value = {
      save: err instanceof Error ? err.message : t('admin.imageCredentials.form.errors.save'),
    }
  } finally {
    submitting.value = false
  }
}

// ==================== Test / Delete ====================

async function onTestCredential(id: number) {
  testingId.value = id
  try {
    const result = await adminImageGenerationAPI.testCredential(id)
    testResult.value = result
    // 测试后刷新该凭据的状态
    await loadCredentials()
  } catch (err) {
    testResult.value = {
      success: false,
      http_status: 0,
      duration_ms: 0,
      error_code: 'CLIENT_ERROR',
      error_message: err instanceof Error ? err.message : String(err),
      key_fingerprint: '—',
    }
  } finally {
    testingId.value = null
  }
}

async function onDeleteCredential(cred: ImageProviderCredential) {
  if (!window.confirm(t('admin.imageCredentials.messages.confirmDelete'))) {
    return
  }
  try {
    await adminImageGenerationAPI.deleteCredential(cred.id)
    credentials.value = credentials.value.filter((c) => c.id !== cred.id)
  } catch (err) {
    console.error(t('admin.imageCredentials.messages.deleteError'), err)
  }
}

// ==================== Lifecycle ====================

onMounted(loadAll)
</script>
