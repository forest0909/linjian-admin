import type { UserConfigExport } from '@tarojs/cli'

export default {
  logger: {
    quiet: false,
    stats: true,
  },
  mini: {},
  h5: {
    devServer: {
      port: 15889,
      open: false,
      // H5 开发时接口走同源，由 devServer 转发到后端。后端不在 :7788 时用 DEV_PROXY_TARGET 指定。
      proxy: {
        '/admin': {
          target: process.env.DEV_PROXY_TARGET || 'http://localhost:7788',
          changeOrigin: true,
        },
      },
    },
  },
} satisfies UserConfigExport<'webpack5'>
