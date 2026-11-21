// babel-preset-taro 的选项见 https://docs.taro.zone/docs/next/babel-config
module.exports = {
  presets: [
    [
      'taro',
      {
        framework: 'react',
        ts: true,
        compiler: 'webpack5',
      },
    ],
  ],
  plugins: [
    // NutUI 按需引入：`import { Button } from '@nutui/nutui-react-taro'` 只打进用到的组件和它的样式。
    // 小程序主包有 2MB 上限，全量样式有 350KB。
    [
      'import',
      {
        libraryName: '@nutui/nutui-react-taro',
        customName: (name) => `@nutui/nutui-react-taro/dist/es/packages/${name.toLowerCase()}`,
        style: 'css',
      },
      'nutui-react-taro',
    ],
  ],
}
