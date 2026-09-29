<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue'
import { api } from '@/api'
import { channelLabelKey, useI18n, type TranslationKey } from '@/i18n'
import type { Account, Cooldown, RequestLog, RequestState, RequestSummary } from '@/types'
import UiIcon from './UiIcon.vue'

const props = defineProps<{
  accounts: Account[]
  cooldowns: Cooldown[]
  requests: RequestSummary[]
  requestLogs: Record<string, RequestLog>
  loading: boolean
  cooldownError: string
  requestError: string
}>()

const emit = defineEmits<{
  refresh: []
  notice: [message: string, tone: 'success' | 'error']
}>()

const { locale, t } = useI18n()
const cancelling = ref('')
const refreshing = ref(false)
const expandedID = ref('')
const copiedKey = ref('')
let copiedTimer: number | undefined

interface RequestBodies {
  requestBody?: string | undefined
  requestTruncated: boolean
  responseBody?: string | undefined
  responseTruncated: boolean
}

// bodiesOf 返回请求的入参与出参正文，未捕获时返回 undefined
function bodiesOf(id: string): RequestBodies | undefined {
  const log = props.requestLogs[id]
  if (log === undefined || (log.request_body === undefined && log.response_body === undefined)) {
    return undefined
  }
  return {
    requestBody: log.request_body,
    requestTruncated: log.request_body_truncated === true,
    responseBody: log.response_body,
    responseTruncated: log.response_body_truncated === true,
  }
}

// expandedBodies 当前展开请求的正文
const expandedBodies = computed(() =>
  expandedID.value === '' ? undefined : bodiesOf(expandedID.value),
)

// hasBodies 判断请求是否已有可查看的正文
function hasBodies(id: string): boolean {
  return bodiesOf(id) !== undefined
}

// prettyBody 尝试格式化 JSON，失败时原样返回
function prettyBody(value: string | undefined): string {
  if (value === undefined) return ''
  try {
    return JSON.stringify(JSON.parse(value), null, 2)
  } catch {
    return value
  }
}

// copyBody 复制正文并短暂提示
async function copyBody(key: string, value: string | undefined): Promise<void> {
  if (value === undefined) return
  try {
    await navigator.clipboard.writeText(value)
    copiedKey.value = key
    if (copiedTimer !== undefined) window.clearTimeout(copiedTimer)
    copiedTimer = window.setTimeout(() => {
      copiedKey.value = ''
    }, 1500)
  } catch {
    // 剪贴板不可用时保持原文可选中复制
  }
}

onUnmounted(() => {
  if (copiedTimer !== undefined) window.clearTimeout(copiedTimer)
})

const requestStateKeys: Record<RequestState, TranslationKey> = {
  queued: 'state.queued',
  running: 'state.running',
  completed: 'state.completed',
  cancelled: 'state.cancelled',
  failed: 'state.failed',
}

const activeCount = computed(
  () =>
    props.requests.filter((request) => request.state === 'queued' || request.state === 'running')
      .length,
)

// accountLabel 将稳定账户 ID 映射为用户显示名称
function accountLabel(id: string, label: string): string {
  return label || props.accounts.find((account) => account.id === id)?.label || '—'
}

// formatTime 根据当前语言显示请求时间
function formatTime(value: string): string {
  return new Intl.DateTimeFormat(locale.value, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).format(new Date(value))
}

// refresh 请求刷新数据，图标短暂旋转确认点击
function refresh(): void {
  refreshing.value = true
  emit('refresh')
  window.setTimeout(() => (refreshing.value = false), 600)
}

// cancelRequest 停止活动请求并刷新摘要
async function cancelRequest(request: RequestSummary): Promise<void> {
  cancelling.value = request.id
  try {
    await api.cancelRequest(request.id)
    emit('refresh')
  } catch (error) {
    emit('notice', error instanceof Error ? error.message : t('common.error'), 'error')
  } finally {
    cancelling.value = ''
  }
}
</script>

<template>
  <section class="mx-auto w-full max-w-4xl flex-1 overflow-auto p-4 md:p-8">
    <div class="mb-6 flex items-center justify-between border-b border-[#30363d] pb-2">
      <h2 class="text-2xl font-bold text-white">{{ t('section.requests.title') }}</h2>
      <button
        class="flex items-center gap-1 rounded bg-blue-600 px-3 py-1.5 text-xs text-white transition hover:bg-blue-500"
        type="button"
        @click="refresh"
      >
        <UiIcon name="refresh" :size="13" :class="{ 'animate-spin': refreshing }" />
        {{ t('app.refresh') }}
      </button>
    </div>

    <div class="space-y-6">
      <article class="rounded-lg border border-[#30363d] bg-[#161b22] p-4">
        <div class="mb-4 flex items-center justify-between">
          <h3 class="font-bold text-gray-300">{{ t('cooldowns.title') }}</h3>
          <span class="font-mono text-xs text-gray-500">{{ cooldowns.length }}</span>
        </div>
        <div
          v-if="cooldownError"
          class="rounded border border-red-500/40 bg-red-500/10 p-3 text-red-300"
        >
          {{ cooldownError }}
        </div>
        <div v-else-if="loading" class="py-8 text-center text-gray-500">
          {{ t('common.loading') }}
        </div>
        <div v-else-if="cooldowns.length === 0" class="py-8 text-center text-xs text-gray-600">
          {{ t('cooldowns.empty') }}
        </div>
        <div v-else class="space-y-3">
          <div
            v-for="cooldown in cooldowns"
            :key="`${cooldown.account_id}:${cooldown.channel}:${cooldown.model_id}`"
            class="overflow-hidden rounded border border-[#30363d] bg-[#0d1117]"
          >
            <div
              class="flex items-center justify-between gap-3 border-b border-[#30363d] bg-[#21262d] px-4 py-2"
            >
              <div class="min-w-0">
                <strong class="block truncate text-sm text-gray-200">{{
                  cooldown.model_id
                }}</strong>
                <span class="text-xs text-gray-500"
                  >{{ accountLabel(cooldown.account_id, cooldown.account_label) }} ·
                  {{ t(channelLabelKey(cooldown.channel)) }}</span
                >
              </div>
              <span class="shrink-0 text-xs text-yellow-400">
                {{ t('state.cooldown') }}
              </span>
            </div>
            <div class="flex flex-wrap justify-between gap-2 p-3 text-xs text-gray-500">
              <span class="min-w-0 break-words">{{ cooldown.reason || '—' }}</span>
              <span>{{ t('cooldowns.until') }}: {{ formatTime(cooldown.until) }}</span>
            </div>
          </div>
        </div>
      </article>

      <article class="overflow-hidden rounded-lg border border-[#30363d] bg-[#161b22]">
        <div class="flex items-center justify-between border-b border-[#30363d] px-4 py-3">
          <h3 class="font-bold text-gray-300">{{ t('requests.history') }}</h3>
          <span class="text-xs text-green-400"> {{ t('requests.live') }}: {{ activeCount }} </span>
        </div>
        <div
          v-if="requestError"
          class="m-4 rounded border border-red-500/40 bg-red-500/10 p-3 text-red-300"
        >
          {{ requestError }}
        </div>
        <div v-else-if="loading" class="py-8 text-center text-gray-500">
          {{ t('common.loading') }}
        </div>
        <div v-else-if="requests.length === 0" class="py-8 text-center text-xs text-gray-600">
          {{ t('requests.empty') }}
        </div>
        <div v-else class="space-y-1 p-2">
          <div
            v-for="request in requests"
            :key="request.id"
            class="rounded transition hover:bg-[#21262d]"
          >
            <div class="flex flex-wrap items-center justify-between gap-3 p-2">
              <div class="min-w-0 flex-1">
                <div class="flex min-w-0 items-center gap-2">
                  <strong class="truncate text-sm text-gray-300">{{ request.model }}</strong>
                  <span class="text-xs text-gray-500">{{
                    t(requestStateKeys[request.state])
                  }}</span>
                </div>
              </div>
              <div class="text-right text-xs text-gray-500">
                <span class="block"
                  >{{ accountLabel(request.account_id, request.account_label)
                  }}<template v-if="request.channel">
                    · {{ t(channelLabelKey(request.channel)) }}</template
                  ></span
                >
                <time class="font-mono">{{ formatTime(request.started_at) }}</time>
              </div>
              <div class="flex items-center gap-2">
                <button
                  v-if="hasBodies(request.id)"
                  class="rounded border border-[#30363d] px-3 py-1 text-xs text-gray-300 transition hover:bg-[#30363d]"
                  type="button"
                  @click="expandedID = expandedID === request.id ? '' : request.id"
                >
                  {{
                    expandedID === request.id ? t('requests.hideBodies') : t('requests.viewBodies')
                  }}
                </button>
                <button
                  v-if="request.state === 'queued' || request.state === 'running'"
                  class="rounded border border-red-900/50 bg-red-900/30 px-3 py-1 text-xs text-red-400 transition hover:bg-red-900/50 disabled:opacity-50"
                  type="button"
                  :disabled="cancelling !== ''"
                  :aria-busy="cancelling === request.id"
                  @click="cancelRequest(request)"
                >
                  {{ t('requests.stop') }}
                </button>
              </div>
            </div>
            <template v-if="expandedID === request.id && expandedBodies">
              <div class="space-y-3 border-t border-[#30363d] px-3 py-3">
                <div v-if="expandedBodies.requestBody !== undefined">
                  <div class="mb-1 flex items-center justify-between">
                    <span class="text-xs font-medium text-gray-400">{{
                      t('requests.requestBody')
                    }}</span>
                    <div class="flex items-center gap-2">
                      <span v-if="expandedBodies.requestTruncated" class="text-xs text-amber-400">{{
                        t('requests.bodyTruncatedShort')
                      }}</span>
                      <button
                        class="rounded border border-[#30363d] px-2 py-0.5 text-xs text-gray-400 transition hover:bg-[#30363d]"
                        type="button"
                        @click="copyBody(request.id + ':req', expandedBodies.requestBody)"
                      >
                        {{
                          copiedKey === request.id + ':req' ? t('common.copied') : t('common.copy')
                        }}
                      </button>
                    </div>
                  </div>
                  <pre
                    class="max-h-72 overflow-auto rounded border border-[#30363d] bg-[#0d1117] p-2 font-mono text-xs break-all whitespace-pre-wrap text-gray-300"
                    >{{ prettyBody(expandedBodies.requestBody) }}</pre>
                </div>
                <div v-if="expandedBodies.responseBody !== undefined">
                  <div class="mb-1 flex items-center justify-between">
                    <span class="text-xs font-medium text-gray-400">{{
                      t('requests.responseBody')
                    }}</span>
                    <div class="flex items-center gap-2">
                      <span
                        v-if="expandedBodies.responseTruncated"
                        class="text-xs text-amber-400"
                        >{{ t('requests.bodyTruncatedShort') }}</span
                      >
                      <button
                        class="rounded border border-[#30363d] px-2 py-0.5 text-xs text-gray-400 transition hover:bg-[#30363d]"
                        type="button"
                        @click="copyBody(request.id + ':res', expandedBodies.responseBody)"
                      >
                        {{
                          copiedKey === request.id + ':res' ? t('common.copied') : t('common.copy')
                        }}
                      </button>
                    </div>
                  </div>
                  <pre
                    class="max-h-72 overflow-auto rounded border border-[#30363d] bg-[#0d1117] p-2 font-mono text-xs break-all whitespace-pre-wrap text-gray-300"
                    >{{ prettyBody(expandedBodies.responseBody) }}</pre>
                </div>
              </div>
            </template>
          </div>
        </div>
      </article>
    </div>
  </section>
</template>
