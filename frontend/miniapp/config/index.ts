import path from 'node:path'
import { defineConfig, type UserConfigExport } from '@tarojs/cli'
import TsconfigPathsPlugin from 'tsconfig-paths-webpack-plugin'
import devConfig from './dev'
import prodConfig from './prod'

// scripts/check-mock-guard.sh 会校验这句报错，改文案时两边一起改。
const MOCK_IN_BUILD_ERROR = 'TARO_APP_WECHAT_MOCK=true 不能用于生产构建，只能用于 npm run dev:* 的监听构建'

// https://taro-docs.jd.com/docs/next/config
export default defineConfig<'webpack5'>(async (merge) => {
  // mock 只给 `npm run dev:*`（带 --watch 的监听构建）用，其他构建里出现这个开关就直接失败。
  // 不能只看 NODE_ENV：Taro 的非监听构建不管 NODE_ENV 是什么，打出来的都是生产包，
  // 把 NODE_ENV 设成 development 就能绕过去。所以按「是不是监听构建」判断。
  const watching = process.argv.includes('--watch') || process.argv.includes('-w')
  if (process.env.TARO_APP_WECHAT_MOCK === 'true' && (!watching || process.env.NODE_ENV === 'production')) {
    throw new Error(MOCK_IN_BUILD_ERROR)
  }

  const baseConfig: UserConfigExport<'webpack5'> = {
    projectName: 'linjian-miniapp',
    date: '2026-11-21',
    // NutUI 的样式按 375 设计稿写，业务样式按 750 写
    designWidth(input) {
      const file = (input as { file?: string } | undefined)?.file
      if (file && file.replace(/\\+/g, '/').indexOf('@nutui') > -1) {
        return 375
      }
      return 750
    },
    deviceRatio: {
      640: 2.34 / 2,
      750: 1,
      375: 2,
      828: 1.81 / 2,
    },
    sourceRoot: 'src',
    // 两端产物分开：dist/weapp、dist/h5
    outputRoot: `dist/${process.env.TARO_ENV}`,
    plugins: ['@tarojs/plugin-html'],
    defineConstants: {},
    copy: {
      patterns: [],
      options: {},
    },
    framework: 'react',
    compiler: {
      type: 'webpack5',
      prebundle: { enable: false },
    },
    cache: {
      enable: false,
    },
    alias: {
      '@': path.resolve(__dirname, '..', 'src'),
    },
    mini: {
      postcss: {
        pxtransform: {
          enable: true,
          config: {},
        },
        cssModules: {
          enable: false,
        },
      },
      webpackChain(chain) {
        chain.resolve.plugin('tsconfig-paths').use(TsconfigPathsPlugin)
      },
    },
    h5: {
      publicPath: '/',
      staticDirectory: 'static',
      output: {
        filename: 'js/[name].[hash:8].js',
        chunkFilename: 'js/[name].[chunkhash:8].js',
      },
      miniCssExtractPluginOption: {
        ignoreOrder: true,
        filename: 'css/[name].[hash].css',
        chunkFilename: 'css/[name].[chunkhash].css',
      },
      postcss: {
        autoprefixer: {
          enable: true,
          config: {},
        },
        cssModules: {
          enable: false,
        },
      },
      webpackChain(chain) {
        chain.resolve.plugin('tsconfig-paths').use(TsconfigPathsPlugin)
      },
    },
  }

  if (process.env.NODE_ENV === 'development') {
    return merge({}, baseConfig, devConfig)
  }
  return merge({}, baseConfig, prodConfig)
})
