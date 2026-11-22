/// <reference types="@tarojs/taro" />

declare module '*.png'
declare module '*.jpg'
declare module '*.svg'
declare module '*.css'
declare module '*.scss'

declare namespace NodeJS {
  interface ProcessEnv {
    NODE_ENV: 'development' | 'production' | 'test'
    /** 当前构建的平台。本工程只支持 weapp 和 h5。 */
    TARO_ENV: 'weapp' | 'h5'
    /** 小程序 AppID，写进 dist/weapp/project.config.json。 */
    TARO_APP_ID?: string
    /** 后端地址。H5 留空（走同源或 devServer 代理）；小程序必须是完整的 https 地址。 */
    TARO_APP_API_BASE?: string
    /** 口令传输用的 AES key，和管理端的 VITE_AES_KEY 相同。 */
    TARO_APP_AES_KEY?: string
    /** 'true' 时微信接口走 mock。只在开发构建里生效，生产构建会直接失败。 */
    TARO_APP_WECHAT_MOCK?: string
  }
}
