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
      // 后端在 :7788。H5 开发时接口走同源，由 devServer 转发。
      proxy: {
        '/admin': {
          target: 'http://localhost:7788',
          changeOrigin: true,
        },
      },
    },
  },
} satisfies UserConfigExport<'webpack5'>
