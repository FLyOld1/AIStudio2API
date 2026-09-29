<script setup lang="ts">
import { nextTick, onMounted, ref } from 'vue'
import { getAdminKey } from '@/api'
import { useI18n } from '@/i18n'

const emit = defineEmits<{
  save: [value: string]
  clear: []
  close: []
}>()

const { t } = useI18n()
const value = ref(getAdminKey())
const reveal = ref(false)
const input = ref<HTMLInputElement>()

onMounted(async () => {
  await nextTick()
  input.value?.focus()
})
</script>

<template>
  <Transition name="dialog" appear>
    <div
      class="modal-overlay"
      role="dialog"
      aria-modal="true"
      @click.self="emit('close')"
      @keydown.esc="emit('close')"
    >
      <div
        class="mx-4 w-full max-w-sm rounded-lg border border-[#30363d] bg-[#161b22] p-5 shadow-xl"
      >
        <h2 class="text-sm font-semibold text-white">{{ t('adminKey.title') }}</h2>
        <p class="mt-1 text-xs leading-5 text-gray-500">{{ t('adminKey.hint') }}</p>
        <div class="mt-3 flex gap-2">
          <input
            ref="input"
            v-model="value"
            :type="reveal ? 'text' : 'password'"
            class="min-w-0 flex-1 rounded border border-[#30363d] bg-[#0d1117] px-3 py-2 text-white transition focus:border-blue-500 focus:outline-none"
            :placeholder="t('adminKey.placeholder')"
            autocomplete="off"
            @keydown.enter.prevent="emit('save', value)"
          />
          <button
            class="rounded border border-[#30363d] bg-[#21262d] px-3 text-xs text-gray-300 transition hover:bg-[#30363d]"
            type="button"
            @click="reveal = !reveal"
          >
            {{ reveal ? t('settings.hide') : t('settings.reveal') }}
          </button>
        </div>
        <div class="mt-5 flex justify-end gap-2">
          <button
            v-if="getAdminKey() !== ''"
            class="rounded bg-[#21262d] px-4 py-1.5 text-sm text-gray-300 transition hover:bg-[#30363d]"
            type="button"
            @click="emit('clear')"
          >
            {{ t('adminKey.clear') }}
          </button>
          <button
            class="rounded bg-[#21262d] px-4 py-1.5 text-sm text-gray-300 transition hover:bg-[#30363d]"
            type="button"
            @click="emit('close')"
          >
            {{ t('common.cancel') }}
          </button>
          <button
            class="rounded bg-blue-600 px-4 py-1.5 text-sm text-white transition hover:bg-blue-500"
            type="button"
            @click="emit('save', value)"
          >
            {{ t('adminKey.save') }}
          </button>
        </div>
      </div>
    </div>
  </Transition>
</template>
