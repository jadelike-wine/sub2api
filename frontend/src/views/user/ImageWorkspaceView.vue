<template>
  <AppLayout>
    <div class="mx-auto h-[calc(100vh-8rem)] max-w-7xl">
      <!-- Page Header -->
      <div class="mb-3 flex items-center justify-between">
        <div>
          <h2 class="text-xl font-semibold text-gray-900 dark:text-white">
            {{ t('aiImage.workspace.title') }}
          </h2>
          <p class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">
            {{ t('aiImage.workspace.description') }}
          </p>
        </div>
      </div>

      <!-- 资产保存期限提示 -->
      <div class="mb-3 flex items-start gap-2 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-700/60 dark:bg-amber-900/20 dark:text-amber-300">
        <svg class="mt-0.5 h-4 w-4 flex-shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
          <path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126zM12 15.75h.008v.008H12v-.008z" />
        </svg>
        <span>{{ t('aiImage.workspace.retentionNotice') }}</span>
      </div>

      <!-- Workspace Layout -->
      <div class="flex h-[calc(100%-3.5rem)] gap-4 overflow-hidden rounded-2xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
        <!-- Conversation Sidebar -->
        <aside class="flex w-64 flex-col border-r border-gray-200 dark:border-dark-700">
          <div class="border-b border-gray-200 p-3 dark:border-dark-700">
            <button
              type="button"
              class="flex w-full items-center justify-center gap-2 rounded-lg bg-primary-600 px-3 py-2 text-sm font-medium text-white transition hover:bg-primary-700 disabled:cursor-not-allowed disabled:opacity-50"
              :disabled="creatingConversation"
              @click="onCreateConversation"
            >
              <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                <path stroke-linecap="round" stroke-linejoin="round" d="M12 4v16m8-8H4" />
              </svg>
              {{ t('aiImage.workspace.conversations.new') }}
            </button>
          </div>

          <div class="border-b border-gray-200 p-3 dark:border-dark-700">
            <input
              v-model="searchKeyword"
              type="text"
              :placeholder="t('aiImage.workspace.conversations.search')"
              class="w-full rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-900 placeholder-gray-400 focus:border-primary-500 focus:outline-none focus:ring-1 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-900 dark:text-white"
            />
          </div>

          <div class="flex-1 overflow-y-auto">
            <div
              v-if="conversationsLoading && conversations.length === 0"
              class="p-4 text-center text-sm text-gray-500 dark:text-dark-400"
            >
              {{ t('common.loading') }}
            </div>
            <div
              v-else-if="filteredConversations.length === 0"
              class="p-4 text-center text-sm text-gray-500 dark:text-dark-400"
            >
              {{ t('aiImage.workspace.conversations.empty') }}
            </div>
            <ul v-else class="space-y-0.5 p-2">
              <li
                v-for="conv in filteredConversations"
                :key="conv.id"
                class="group flex items-center gap-1 rounded-lg"
                :class="[
                  currentConversation?.id === conv.id
                    ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
                    : 'text-gray-700 hover:bg-gray-100 dark:text-dark-200 dark:hover:bg-dark-700'
                ]"
              >
                <button
                  type="button"
                  class="flex min-w-0 flex-1 items-center gap-2 rounded-lg px-3 py-2 text-left text-sm transition"
                  @click="onSelectConversation(conv.id)"
                >
                  <svg class="h-4 w-4 flex-shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
                    <path stroke-linecap="round" stroke-linejoin="round" d="M7.5 8.25h9.75m-9.75 3.75h9.75m-9.75 3.75h9.75M3.75 5.25v13.5A2.25 2.25 0 006 21h12a2.25 2.25 0 002.25-2.25V5.25A2.25 2.25 0 0018 3H6A2.25 2.25 0 003.75 5.25z" />
                  </svg>
                  <span class="min-w-0 flex-1 truncate">{{ conv.title || t('aiImage.workspace.conversations.defaultTitle') }}</span>
                </button>
                <button
                  type="button"
                  class="flex-shrink-0 px-2 py-2 text-xs text-gray-400 opacity-0 transition group-hover:opacity-100 hover:text-red-500"
                  :title="t('aiImage.workspace.conversations.delete')"
                  @click="onDeleteConversation(conv.id)"
                >
                  <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
                    <path stroke-linecap="round" stroke-linejoin="round" d="M14.74 9l-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 01-2.244 2.077H8.084a2.25 2.25 0 01-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 00-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 013.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 00-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 00-7.5 0" />
                  </svg>
                </button>
              </li>
            </ul>
          </div>
        </aside>

        <!-- Main Conversation Area -->
        <div class="flex flex-1 flex-col overflow-hidden">
          <!-- Header (current conversation title / rename) -->
          <div class="flex items-center justify-between border-b border-gray-200 px-4 py-3 dark:border-dark-700">
            <div class="flex items-center gap-2 min-w-0 flex-1">
              <h3 class="truncate text-sm font-medium text-gray-900 dark:text-white">
                {{ currentConversation?.title || t('aiImage.workspace.conversations.defaultTitle') }}
              </h3>
            </div>
          </div>

          <!-- Generations List -->
          <div class="flex-1 overflow-y-auto p-4">
            <!-- 未选中会话（非 draft 草稿态） -->
            <div
              v-if="!currentConversation && !isDraftConversation"
              class="flex h-full flex-col items-center justify-center text-center"
            >
              <svg class="mb-3 h-12 w-12 text-gray-300 dark:text-dark-600" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
                <path stroke-linecap="round" stroke-linejoin="round" d="M2.25 15.75l5.159-5.159a2.25 2.25 0 013.182 0l5.159 5.159m-1.5-1.5l1.409-1.409a2.25 2.25 0 013.182 0l2.909 2.909m-18 3.75h16.5a1.5 1.5 0 001.5-1.5V6a1.5 1.5 0 00-1.5-1.5H3.75A1.5 1.5 0 002.25 6v12a1.5 1.5 0 001.5 1.5zm10.5-11.25h.008v.008h-.008V8.25zm.375 0a.375.375 0 11-.75 0 .375.375 0 01.75 0z" />
              </svg>
              <p class="text-sm font-medium text-gray-500 dark:text-dark-400">
                {{ t('aiImage.workspace.conversations.empty') }}
              </p>
            </div>
            <!-- 加载中：仅在有会话且正在请求历史记录时显示 -->
            <div
              v-else-if="generationsLoading"
              class="flex h-full items-center justify-center text-sm text-gray-500 dark:text-dark-400"
            >
              <svg class="mr-2 h-5 w-5 animate-spin text-primary-500" fill="none" viewBox="0 0 24 24">
                <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" />
                <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
              </svg>
              {{ t('common.loading') }}
            </div>
            <!-- 加载失败 -->
            <div
              v-else-if="generationsError"
              class="flex h-full flex-col items-center justify-center text-center"
            >
              <svg class="mb-3 h-10 w-10 text-red-400 dark:text-red-500" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
                <path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126zM12 15.75h.008v.008H12v-.008z" />
              </svg>
              <p class="text-sm font-medium text-red-600 dark:text-red-400">{{ generationsError }}</p>
              <button
                type="button"
                class="mt-3 rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-700 hover:bg-gray-50 dark:border-dark-600 dark:bg-dark-800 dark:text-dark-200 dark:hover:bg-dark-700"
                @click="onRetryLoad"
              >
                {{ t('aiImage.workspace.generations.retry') }}
              </button>
            </div>
            <!-- 空状态：会话无生成记录 -->
            <div
              v-else-if="generations.length === 0"
              class="flex h-full flex-col items-center justify-center text-center"
            >
              <svg class="mb-3 h-12 w-12 text-primary-400 dark:text-primary-500" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
                <path stroke-linecap="round" stroke-linejoin="round" d="M9.813 15.904L9 18.75l-.813-2.846a4.5 4.5 0 00-3.09-3.09L2.25 12l2.846-.813a4.5 4.5 0 003.09-3.09L9 5.25l.813 2.846a4.5 4.5 0 003.09 3.09L15.75 12l-2.846.813a4.5 4.5 0 00-3.09 3.09zM18.259 8.715L18 9.75l-.259-1.035a3.375 3.375 0 00-2.455-2.456L14.25 6l1.036-.259a3.375 3.375 0 002.455-2.456L18 2.25l.259 1.035a3.375 3.375 0 002.456 2.456L21.75 6l-1.035.259a3.375 3.375 0 00-2.456 2.456z" />
              </svg>
              <p class="text-base font-medium text-gray-900 dark:text-white">
                {{ t('aiImage.workspace.generations.startCreating') }}
              </p>
              <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">
                {{ t('aiImage.workspace.generations.startCreatingHint') }}
              </p>
            </div>
            <div v-else class="space-y-6">
              <article
                v-for="gen in generations"
                :key="gen.id"
                class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800"
              >
                <!-- Generation meta -->
                <header class="mb-3 flex items-start justify-between gap-2">
                  <div class="min-w-0 flex-1">
                    <p class="text-sm font-medium text-gray-900 dark:text-white">{{ gen.prompt }}</p>
                    <div class="mt-1 flex flex-wrap items-center gap-2 text-xs text-gray-500 dark:text-dark-400">
                      <span class="rounded-full bg-gray-100 px-2 py-0.5 dark:bg-dark-700">
                        {{ gen.generation_type === 'text_to_image' ? t('aiImage.workspace.composer.typeTextToImage') : t('aiImage.workspace.composer.typeImageToImage') }}
                      </span>
                      <span class="rounded-full bg-gray-100 px-2 py-0.5 dark:bg-dark-700">{{ gen.size }}</span>
                      <span v-if="gen.ratio" class="rounded-full bg-gray-100 px-2 py-0.5 dark:bg-dark-700">{{ gen.ratio }}</span>
                      <StatusBadge :status="gen.status" />
                      <span v-if="gen.status === 'succeeded' && gen.duration_ms > 0">
                        {{ t('aiImage.workspace.generations.duration', { ms: gen.duration_ms }) }}
                      </span>
                    </div>
                  </div>
                </header>

                <!-- Input images (img2img) -->
                <div v-if="gen.generation_type === 'image_to_image' && getInputAssets(gen).length > 0" class="mb-3">
                  <p class="mb-1.5 text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('aiImage.workspace.generations.inputImages') }}</p>
                  <div class="flex flex-wrap gap-2">
                    <el-image
                      v-for="asset in getInputAssets(gen)"
                      :key="asset.id"
                      :src="asset.url"
                      :alt="asset.original_filename || 'input'"
                      fit="cover"
                      :preview-src-list="getInputAssetUrls(gen)"
                      :initial-index="getInputAssetIndex(gen, asset.id)"
                      :preview-teleported="true"
                      hide-on-click-modal
                      class="h-20 w-20 cursor-zoom-in overflow-hidden rounded-lg border border-gray-200 dark:border-dark-600"
                    >
                      <template #placeholder>
                        <div class="flex h-20 w-20 flex-col items-center justify-center gap-1 bg-gray-100 dark:bg-dark-700">
                          <svg class="h-4 w-4 animate-spin text-primary-500" fill="none" viewBox="0 0 24 24">
                            <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" />
                            <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
                          </svg>
                          <span class="text-[10px] text-gray-500 dark:text-dark-400">{{ t('aiImage.workspace.generations.imageLoading') }}</span>
                        </div>
                      </template>
                    </el-image>
                  </div>
                </div>

                <!-- Output images (succeeded) -->
                <div v-if="gen.status === 'succeeded'">
                  <template v-if="getOutputAssets(gen).length > 0">
                    <p class="mb-1.5 text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('aiImage.workspace.generations.outputImages') }}</p>
                    <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
                      <div
                        v-for="asset in getOutputAssets(gen)"
                        :key="asset.id"
                        class="group relative overflow-hidden rounded-xl border border-gray-200 bg-gray-50 dark:border-dark-600 dark:bg-dark-900"
                      >
                        <el-image
                          :src="asset.url"
                          :alt="`output-${asset.id}`"
                          fit="contain"
                          :preview-src-list="getOutputAssetUrls(gen)"
                          :initial-index="getOutputAssetIndex(gen, asset.id)"
                          :preview-teleported="true"
                          hide-on-click-modal
                          class="w-full cursor-zoom-in"
                        >
                          <template #placeholder>
                            <div class="flex aspect-square w-full flex-col items-center justify-center gap-2 bg-gray-100 dark:bg-dark-900">
                              <svg class="h-6 w-6 animate-spin text-primary-500" fill="none" viewBox="0 0 24 24">
                                <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" />
                                <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
                              </svg>
                              <span class="text-xs text-gray-500 dark:text-dark-400">{{ t('aiImage.workspace.generations.imageLoading') }}</span>
                            </div>
                          </template>
                        </el-image>
                        <div class="absolute inset-x-0 bottom-0 flex justify-end gap-2 bg-gradient-to-t from-black/60 to-transparent p-2 opacity-0 transition group-hover:opacity-100">
                          <a
                            :href="asset.url"
                            download
                            class="rounded-md bg-white/90 px-2 py-1 text-xs font-medium text-gray-900 hover:bg-white"
                            target="_blank"
                            rel="noopener noreferrer"
                          >
                            {{ t('aiImage.workspace.generations.download') }}
                          </a>
                          <button
                            type="button"
                            class="rounded-md bg-white/90 px-2 py-1 text-xs font-medium text-gray-900 hover:bg-white"
                            @click="onRefreshAssetURL(asset.id)"
                          >
                            {{ t('aiImage.workspace.generations.refreshUrl') }}
                          </button>
                        </div>
                      </div>
                    </div>
                  </template>
                  <!-- succeeded 但没有图片资源：显示 fallback 提示，避免空白 -->
                  <div
                    v-else
                    class="flex items-center justify-center rounded-xl border border-dashed border-amber-300 bg-amber-50 py-10 dark:border-amber-700 dark:bg-amber-900/20"
                  >
                    <div class="text-center">
                      <svg class="mx-auto mb-2 h-8 w-8 text-amber-500 dark:text-amber-400" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
                        <path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126zM12 15.75h.008v.008H12v-.008z" />
                      </svg>
                      <p class="text-sm font-medium text-amber-700 dark:text-amber-300">
                        {{ t('aiImage.workspace.generations.assetsNotFound') }}
                      </p>
                      <p class="mt-1 text-xs text-amber-600/80 dark:text-amber-400/80">
                        {{ t('aiImage.workspace.generations.assetsNotFoundHint') }}
                      </p>
                    </div>
                  </div>
                </div>

                <!-- Pending / Processing -->
                <div
                  v-else-if="gen.status === 'pending' || gen.status === 'processing'"
                  class="flex items-center justify-center rounded-xl border border-dashed border-gray-300 bg-gray-50 py-12 dark:border-dark-600 dark:bg-dark-900"
                >
                  <div class="flex items-center gap-2 text-sm text-gray-500 dark:text-dark-400">
                    <svg class="h-5 w-5 animate-spin text-primary-500" fill="none" viewBox="0 0 24 24">
                      <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" />
                      <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
                    </svg>
                    {{ t('aiImage.workspace.composer.generating') }}
                  </div>
                </div>

                <!-- Failed -->
                <div
                  v-else-if="gen.status === 'failed'"
                  class="rounded-xl border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900 dark:bg-red-900/20 dark:text-red-300"
                >
                  <p class="font-medium">{{ t('aiImage.workspace.status.failed') }}</p>
                  <p class="mt-1 text-xs">{{ imageErrorMessage(gen.error_code) }}</p>
                </div>
              </article>
            </div>
          </div>

          <!-- Composer（选中会话或新建会话草稿态时显示） -->
          <div
            v-if="currentConversation || isDraftConversation"
            class="border-t border-gray-200 p-4 dark:border-dark-700"
          >
            <!-- 单轮限制提示：当前会话已有 generation 时禁止再次提交 -->
            <div
              v-if="hasGeneratedOnce"
              class="flex items-center justify-center rounded-lg border border-dashed border-gray-300 bg-gray-50 py-3 text-center dark:border-dark-600 dark:bg-dark-900"
            >
              <div>
                <p class="text-sm font-medium text-gray-700 dark:text-dark-200">
                  {{ t('aiImage.workspace.composer.singleRoundLimit') }}
                </p>
                <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
                  {{ t('aiImage.workspace.composer.singleRoundLimitHint') }}
                </p>
              </div>
            </div>
            <template v-else>
            <!-- Uploaded reference images (img2img only) -->
            <div v-if="pendingInputAssets.length > 0" class="mb-3 flex flex-wrap gap-2">
              <div
                v-for="asset in pendingInputAssets"
                :key="asset.id"
                class="group relative overflow-hidden rounded-lg border border-gray-200 dark:border-dark-600"
              >
                <el-image
                  :src="asset.url"
                  :alt="asset.original_filename || 'input'"
                  fit="cover"
                  :preview-src-list="getPendingInputAssetUrls()"
                  :initial-index="pendingInputAssets.findIndex((a) => a.id === asset.id)"
                  class="h-24 w-24 cursor-zoom-in overflow-hidden rounded-lg border border-gray-200 dark:border-dark-600"
                >
                  <template #placeholder>
                    <div class="flex h-24 w-24 flex-col items-center justify-center gap-1 bg-gray-100 dark:bg-dark-700">
                      <svg class="h-5 w-5 animate-spin text-primary-500" fill="none" viewBox="0 0 24 24">
                        <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" />
                        <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
                      </svg>
                      <span class="text-[10px] text-gray-500 dark:text-dark-400">{{ t('aiImage.workspace.generations.imageLoading') }}</span>
                    </div>
                  </template>
                </el-image>
                <button
                  type="button"
                  class="absolute right-0 top-0 rounded-bl-md bg-black/60 px-1 text-xs text-white opacity-0 transition group-hover:opacity-100"
                  @click="onRemovePendingAsset(asset.id)"
                >
                  ×
                </button>
              </div>
            </div>

            <!-- Controls -->
            <div class="mb-2 flex flex-wrap items-center gap-3">
              <!-- Generation type toggle -->
              <div class="flex rounded-lg border border-gray-300 p-0.5 dark:border-dark-600">
                <button
                  type="button"
                  class="rounded-md px-2.5 py-1 text-xs font-medium transition"
                  :class="generationType === 'text_to_image'
                    ? 'bg-primary-600 text-white'
                    : 'text-gray-600 hover:bg-gray-100 dark:text-dark-300 dark:hover:bg-dark-700'"
                  @click="generationType = 'text_to_image'"
                >
                  {{ t('aiImage.workspace.composer.typeTextToImage') }}
                </button>
                <button
                  type="button"
                  class="rounded-md px-2.5 py-1 text-xs font-medium transition"
                  :class="generationType === 'image_to_image'
                    ? 'bg-primary-600 text-white'
                    : 'text-gray-600 hover:bg-gray-100 dark:text-dark-300 dark:hover:bg-dark-700'"
                  @click="generationType = 'image_to_image'"
                >
                  {{ t('aiImage.workspace.composer.typeImageToImage') }}
                </button>
              </div>

              <!-- Size selector -->
              <select
                v-model="size"
                class="rounded-lg border border-gray-300 bg-white px-2 py-1 text-xs text-gray-900 focus:border-primary-500 focus:outline-none dark:border-dark-600 dark:bg-dark-900 dark:text-white"
              >
                <option v-for="s in IMAGE_GENERATION_SIZES" :key="s" :value="s">{{ s }}</option>
              </select>

              <!-- Ratio selector -->
              <select
                v-model="ratio"
                class="rounded-lg border border-gray-300 bg-white px-2 py-1 text-xs text-gray-900 focus:border-primary-500 focus:outline-none dark:border-dark-600 dark:bg-dark-900 dark:text-white"
              >
                <option v-for="r in IMAGE_GENERATION_RATIOS" :key="r" :value="r">{{ r }}</option>
              </select>

              <!-- Upload (only for img2img) -->
              <label
                v-if="generationType === 'image_to_image'"
                class="flex cursor-pointer items-center gap-1 rounded-lg border border-gray-300 px-2.5 py-1 text-xs font-medium text-gray-700 hover:bg-gray-100 dark:border-dark-600 dark:text-dark-300 dark:hover:bg-dark-700"
                :class="{ 'cursor-not-allowed opacity-50': uploadingAsset || pendingInputAssets.length >= MAX_INPUTS }"
              >
                <svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
                  <path stroke-linecap="round" stroke-linejoin="round" d="M3 16.5v2.25A2.25 2.25 0 005.25 21h13.5A2.25 2.25 0 0021 18.75V16.5M16.5 12L12 16.5m0 0L7.5 12m4.5 4.5V3" />
                </svg>
                {{ t('aiImage.workspace.composer.upload') }}
                <input
                  type="file"
                  accept="image/png,image/jpeg,image/webp"
                  multiple
                  class="hidden"
                  :disabled="uploadingAsset || pendingInputAssets.length >= MAX_INPUTS"
                  @change="onUploadInputAsset"
                />
              </label>
            </div>

            <!-- Upload hint -->
            <p v-if="generationType === 'image_to_image'" class="mb-2 text-xs text-gray-400 dark:text-dark-500">
              {{ t('aiImage.workspace.composer.uploadHint') }}
            </p>

            <!-- Prompt input -->
            <div class="flex gap-2">
              <textarea
                v-model="prompt"
                rows="2"
                :placeholder="t('aiImage.workspace.composer.promptPlaceholder')"
                class="flex-1 resize-none rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 placeholder-gray-400 focus:border-primary-500 focus:outline-none focus:ring-1 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-900 dark:text-white"
                @keydown.enter.exact.prevent="onCreateGeneration"
              />
              <button
                type="button"
                class="flex items-center gap-1 self-end rounded-lg bg-primary-600 px-4 py-2 text-sm font-medium text-white transition hover:bg-primary-700 disabled:cursor-not-allowed disabled:opacity-50"
                :disabled="!canSubmit"
                @click="onCreateGeneration"
              >
                <svg v-if="creatingGeneration" class="h-4 w-4 animate-spin" fill="none" viewBox="0 0 24 24">
                  <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" />
                  <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
                </svg>
                <span>{{ creatingGeneration ? t('aiImage.workspace.composer.sending') : t('aiImage.workspace.composer.send') }}</span>
              </button>
            </div>

            <!-- Error banner -->
            <p v-if="composerError" class="mt-2 text-xs text-red-600 dark:text-red-400">{{ composerError }}</p>
            </template>
          </div>
        </div>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import StatusBadge from '@/components/image/StatusBadge.vue'
import { useImageGenerationStore } from '@/stores/imageGeneration'
import {
  IMAGE_GENERATION_INPUT_MIME_TYPES,
  IMAGE_GENERATION_RATIOS,
  IMAGE_GENERATION_SIZES,
  type ImageAsset,
  type ImageGeneration,
  type ImageGenerationType,
} from '@/types'

const { t } = useI18n()
const store = useImageGenerationStore()

// errorCode → i18n key 映射，用于将后端 error_code 转换为用户友好的提示文案。
// 后端错误码定义见 backend/internal/service/image_generation_errors.go。
const IMAGE_ERROR_CODE_TO_I18N_KEY: Record<string, string> = {
  IMAGE_INSUFFICIENT_BALANCE: 'insufficientBalance',
  IMAGE_CONCURRENT_LIMIT: 'concurrentLimit',
  IMAGE_PROVIDER_ERROR: 'upstream',
  IMAGE_PROVIDER_TIMEOUT: 'providerTimeout',
  IMAGE_PROVIDER_RATE_LIMITED: 'providerRateLimited',
  IMAGE_PROVIDER_AUTH_FAILED: 'providerAuthFailed',
  IMAGE_DOWNLOAD_FAILED: 'downloadFailed',
  IMAGE_STORAGE_FAILED: 'storageFailed',
  IMAGE_INVALID_REQUEST: 'invalidRequest',
  IMAGE_PROMPT_REQUIRED: 'promptRequired',
  IMAGE_INVALID_SIZE: 'invalidSize',
  IMAGE_INVALID_RATIO: 'invalidRatio',
  IMAGE_INPUT_REQUIRED: 'inputRequired',
  IMAGE_INPUT_TOO_LARGE: 'inputTooLarge',
  IMAGE_INPUT_UNSUPPORTED: 'inputUnsupported',
  IMAGE_NO_AVAILABLE_CREDENTIAL: 'noCredential',
  IMAGE_QUOTA_EXCEEDED: 'quotaExceeded',
  IMAGE_TASK_ALREADY_RUNNING: 'taskAlreadyRunning',
  IMAGE_GENERATION_DISABLED: 'disabled',
  IMAGE_ACCESS_DENIED: 'forbidden',
  IMAGE_GENERATION_NOT_FOUND: 'notFound',
  IMAGE_CONVERSATION_NOT_FOUND: 'notFound',
  IMAGE_ASSET_NOT_FOUND: 'notFound',
}

// imageErrorMessage 根据 error_code 返回友好的本地化错误提示。
// errorCode 为空或未匹配时回退到 unknown。
function imageErrorMessage(errorCode?: string | null): string {
  if (!errorCode) return t('aiImage.workspace.errors.unknown')
  const key = IMAGE_ERROR_CODE_TO_I18N_KEY[errorCode]
  if (!key) return t('aiImage.workspace.errors.unknown')
  return t(`aiImage.workspace.errors.${key}`)
}

// Local component state
const searchKeyword = ref('')
const prompt = ref('')
const generationType = ref<ImageGenerationType>('text_to_image')
const size = ref<string>(IMAGE_GENERATION_SIZES[0])
const ratio = ref<string>(IMAGE_GENERATION_RATIOS[0])
const composerError = ref<string | null>(null)
const generationsError = ref<string | null>(null)

const MAX_INPUTS = 6
const MAX_FILE_SIZE = 10 * 1024 * 1024 // 10 MB

// Store-backed state (kept as computed for template reactivity)
const conversations = computed(() => store.conversations)
const conversationsLoading = computed(() => store.conversationsLoading)
const currentConversation = computed(() => store.currentConversation)
const generations = computed(() => store.generations)
const generationsLoading = computed(() => store.generationsLoading)
const isDraftConversation = computed(() => store.isDraftConversation)
const pendingInputAssets = computed(() => store.pendingInputAssets)
const creatingConversation = computed(() => store.creatingGeneration && !currentConversation.value)
const creatingGeneration = computed(() => store.creatingGeneration)
const uploadingAsset = computed(() => store.uploadingAsset)

const filteredConversations = computed(() => {
  const kw = searchKeyword.value.trim().toLowerCase()
  if (!kw) return conversations.value
  return conversations.value.filter((c) => (c.title || '').toLowerCase().includes(kw))
})

/** 单轮限制：当前会话已有 generation 时为 true，禁止再次提交 */
const hasGeneratedOnce = computed(() => generations.value.length > 0)

const canSubmit = computed(() => {
  if (creatingGeneration.value) return false
  if (!prompt.value.trim()) return false
  if (generationType.value === 'image_to_image' && pendingInputAssets.value.length === 0) {
    return false
  }
  return true
})

// ==================== Asset Helpers ====================

function getInputAssets(gen: ImageGeneration): ImageAsset[] {
  return gen.input_assets ?? []
}

/** 输入图片的 URL 列表，供 el-image 预览使用 */
function getInputAssetUrls(gen: ImageGeneration): string[] {
  return getInputAssets(gen).map((a) => a.url).filter((u): u is string => !!u)
}

/** 当前输入 asset 在预览列表中的索引（el-image 的 initial-index） */
function getInputAssetIndex(gen: ImageGeneration, assetId: number): number {
  const idx = getInputAssets(gen).findIndex((a) => a.id === assetId)
  return idx >= 0 ? idx : 0
}

function getOutputAssets(gen: ImageGeneration): ImageAsset[] {
  return gen.output_assets ?? []
}

/** 输出图片的 URL 列表，供 el-image 预览使用 */
function getOutputAssetUrls(gen: ImageGeneration): string[] {
  return getOutputAssets(gen).map((a) => a.url).filter((u): u is string => !!u)
}

/** 当前 asset 在预览列表中的索引（el-image 的 initial-index） */
function getOutputAssetIndex(gen: ImageGeneration, assetId: number): number {
  const idx = getOutputAssets(gen).findIndex((a) => a.id === assetId)
  return idx >= 0 ? idx : 0
}

/** Composer 待上传参考图的 URL 列表（预览用） */
function getPendingInputAssetUrls(): string[] {
  return pendingInputAssets.value.map((a) => a.url).filter((u): u is string => !!u)
}

// ==================== Conversation Actions ====================

async function onCreateConversation() {
  generationsError.value = null
  // 仅在前端清空状态进入"新会话"草稿态，不调 API 持久化。
  // 真正的会话在首次提交生成任务时由后端自动创建（标题取 prompt 前 30 字）。
  store.startNewConversation()
}

async function onSelectConversation(id: number) {
  generationsError.value = null
  try {
    await store.selectConversation(id)
  } catch (err) {
    console.error(err)
    generationsError.value = err instanceof Error ? err.message : t('aiImage.workspace.errors.unknown')
  }
}

async function onRetryLoad() {
  if (!currentConversation.value) return
  generationsError.value = null
  try {
    await store.selectConversation(currentConversation.value.id)
  } catch (err) {
    console.error(err)
    generationsError.value = err instanceof Error ? err.message : t('aiImage.workspace.errors.unknown')
  }
}

async function onDeleteConversation(id: number) {
  if (!window.confirm(t('aiImage.workspace.conversations.confirmDelete'))) {
    return
  }
  try {
    await store.deleteConversation(id)
  } catch (err) {
    console.error(err)
  }
}

// ==================== Generation Actions ====================

async function onCreateGeneration() {
  composerError.value = null
  if (!prompt.value.trim()) {
    composerError.value = t('aiImage.workspace.composer.promptRequired')
    return
  }
  if (generationType.value === 'image_to_image' && pendingInputAssets.value.length === 0) {
    composerError.value = t('aiImage.workspace.composer.uploadHint')
    return
  }

  try {
    await store.createGeneration({
      conversation_id: currentConversation.value?.id ?? null,
      type: generationType.value,
      prompt: prompt.value.trim(),
      size: size.value,
      ratio: ratio.value,
      input_asset_ids:
        generationType.value === 'image_to_image'
          ? pendingInputAssets.value.map((a) => a.id)
          : undefined,
    })
    prompt.value = ''
  } catch (err: any) {
    // 优先使用后端 error_code 映射到友好提示，未匹配时回退到 unknown
    const reason = err?.reason as string | undefined
    if (reason && IMAGE_ERROR_CODE_TO_I18N_KEY[reason]) {
      composerError.value = imageErrorMessage(reason)
    } else {
      composerError.value = err instanceof Error ? err.message : t('aiImage.workspace.errors.unknown')
    }
  }
}

async function onRefreshAssetURL(assetId: number) {
  try {
    await store.refreshAssetURL(assetId)
  } catch (err) {
    console.error(err)
  }
}

// ==================== Asset Upload ====================

async function onUploadInputAsset(event: Event) {
  composerError.value = null
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  // Reset input so the same file can be re-selected later
  input.value = ''
  if (files.length === 0) return

  // 预校验：类型 / 大小 / 数量上限
  const validFiles: File[] = []
  for (const file of files) {
    if (!IMAGE_GENERATION_INPUT_MIME_TYPES.includes(file.type as typeof IMAGE_GENERATION_INPUT_MIME_TYPES[number])) {
      composerError.value = t('aiImage.workspace.composer.invalidType')
      return
    }
    if (file.size > MAX_FILE_SIZE) {
      composerError.value = t('aiImage.workspace.composer.fileTooLarge')
      return
    }
    validFiles.push(file)
  }
  if (pendingInputAssets.value.length + validFiles.length > MAX_INPUTS) {
    composerError.value = t('aiImage.workspace.composer.maxInputs')
    return
  }

  // 串行上传（避免并发占用过多连接，错误时停止后续上传）
  try {
    for (const file of validFiles) {
      await store.uploadInputAsset(file)
    }
  } catch (err) {
    composerError.value = err instanceof Error ? err.message : t('aiImage.workspace.composer.uploadError')
  }
}

function onRemovePendingAsset(assetId: number) {
  store.removePendingInputAsset(assetId)
}

// ==================== Lifecycle ====================

onMounted(async () => {
  try {
    await store.fetchConversations()
  } catch (err) {
    console.error(err)
  }
})

onUnmounted(() => {
  // Stop all polling when leaving the workspace; conversations data is retained
  // in store so returning to the page won't re-fetch from scratch immediately.
  store.stopAllPolling()
})
</script>
