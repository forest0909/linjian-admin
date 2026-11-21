import path from 'node:path'
import { defineConfig } from 'vitest/config'

// 单测只覆盖 src/core 这类不依赖页面渲染的逻辑，Taro 的 API 在用例里 mock。
export default defineConfig({
  resolve: {
    alias: {
      '@': path.resolve(__dirname, 'src'),
    },
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
    passWithNoTests: true,
  },
})
