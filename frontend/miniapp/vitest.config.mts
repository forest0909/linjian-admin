import path from 'node:path'
import { defineConfig } from 'vitest/config'

// 单测只覆盖 src/core 这类不依赖页面渲染的逻辑，Taro 的 API 在用例里 mock。
export default defineConfig({
  resolve: {
    alias: {
      '@': path.resolve(import.meta.dirname, 'src'),
    },
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
    env: {
      NODE_ENV: 'test',
      TARO_ENV: 'weapp',
      TARO_APP_API_BASE: '',
      TARO_APP_AES_KEY: 'f51d66a73d8a0927',
      TARO_APP_WECHAT_MOCK: 'false',
    },
  },
})
