import { useCallback, useEffect, useState } from 'react'
import { Button as NativeButton, Image, Text, View } from '@tarojs/components'
import { Button, Input } from '@nutui/nutui-react-taro'
import Taro from '@tarojs/taro'
import { fetchCaptcha, loginByPassword, loginByPhone, type Captcha } from '@/api/auth'
import { ApiError } from '@/core/errors'
import { HOME_PAGE } from '@/core/session'
import { getLoginCode, mockPhoneCode, phoneCodeFromEvent, WECHAT_AVAILABLE, WECHAT_MOCK } from '@/core/wechat'
import './index.scss'

type Mode = 'wechat' | 'password'

function toast(title: string) {
  Taro.showToast({ title, icon: 'none' })
}

function enterHome() {
  Taro.reLaunch({ url: HOME_PAGE }).catch((err) => {
    console.error('[login] 跳转首页失败', err)
  })
}

export default function Login() {
  // 有微信能力时默认一键登录；H5 只有账号密码。
  const [mode, setMode] = useState<Mode>(WECHAT_AVAILABLE ? 'wechat' : 'password')
  const [submitting, setSubmitting] = useState(false)

  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [captchaValue, setCaptchaValue] = useState('')
  const [captcha, setCaptcha] = useState<Captcha | null>(null)
  const [mockMobile, setMockMobile] = useState('')

  const refreshCaptcha = useCallback(async () => {
    setCaptchaValue('')
    try {
      setCaptcha(await fetchCaptcha())
    } catch (err) {
      // 请求封装已经提示过，这里只留日志。点验证码区域可以重试。
      console.error('[login] 获取验证码失败', err)
      setCaptcha(null)
    }
  }, [])

  useEffect(() => {
    if (mode === 'password') {
      refreshCaptcha()
    }
  }, [mode, refreshCaptcha])

  async function phoneLogin(phoneCode: string) {
    setSubmitting(true)
    try {
      const code = await getLoginCode()
      await loginByPhone(code, phoneCode)
      enterHome()
    } catch (err) {
      console.error('[login] 一键登录失败', err)
      // 接口错误请求封装已经提示；wx.login 自己失败时补一句。
      if (!(err instanceof ApiError)) {
        toast('微信登录暂不可用，请稍后重试')
      }
    } finally {
      setSubmitting(false)
    }
  }

  function onGetPhoneNumber(e: { detail: { code?: string; errMsg?: string } }) {
    const phoneCode = phoneCodeFromEvent(e.detail)
    if (!phoneCode) {
      toast('需要授权手机号才能一键登录')
      return
    }
    phoneLogin(phoneCode)
  }

  function onMockPhoneLogin() {
    if (!/^1\d{10}$/.test(mockMobile)) {
      toast('请输入 11 位手机号')
      return
    }
    phoneLogin(mockPhoneCode(mockMobile))
  }

  async function onPasswordLogin() {
    if (!username.trim() || !password) {
      toast('请输入账号和密码')
      return
    }
    if (!captcha || !captchaValue.trim()) {
      toast('请输入验证码')
      return
    }
    setSubmitting(true)
    try {
      let code: string | undefined
      if (WECHAT_AVAILABLE) {
        try {
          code = await getLoginCode()
        } catch (err) {
          // 拿不到微信 code 只是这次不绑定微信，不挡账号密码登录。
          console.error('[login] wx.login 失败，本次登录不绑定微信', err)
        }
      }
      await loginByPassword({
        username: username.trim(),
        password,
        captchaId: captcha.captchaId,
        captchaValue: captchaValue.trim(),
        code,
      })
      enterHome()
    } catch (err) {
      console.error('[login] 账号密码登录失败', err)
      // 验证码只能用一次，失败后换一张。
      refreshCaptcha()
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <View className='login'>
      <View className='login__brand'>
        <View className='login__logo'>
          <Text className='login__logo-text'>林</Text>
        </View>
        <Text className='login__title'>林间 Admin</Text>
        <Text className='login__subtitle'>{mode === 'wechat' ? '使用微信绑定的手机号快捷登录' : '使用后台账号登录'}</Text>
      </View>

      {mode === 'wechat' ? (
        <View className='login__panel'>
          {process.env.NODE_ENV !== 'production' && WECHAT_MOCK ? (
            <>
              <View className='login__field login__field--last'>
                <Input
                  type='digit'
                  maxLength={11}
                  placeholder='手机号（mock：填后台已有用户的手机号）'
                  value={mockMobile}
                  onChange={setMockMobile}
                />
              </View>
              <Button type='primary' size='large' block loading={submitting} disabled={submitting} onClick={onMockPhoneLogin}>
                微信一键登录（mock）
              </Button>
            </>
          ) : (
            // 手机号授权只有小程序原生 button 的 open-type 能触发。NutUI 的 Button 渲染成 view，不能用在这里。
            <NativeButton
              className='login__wechat-btn'
              hoverClass='login__wechat-btn--hover'
              loading={submitting}
              disabled={submitting}
              openType='getPhoneNumber'
              onGetPhoneNumber={onGetPhoneNumber}
            >
              微信一键登录
            </NativeButton>
          )}
          <Text className='login__hint'>手机号需要已在后台开通账号</Text>
        </View>
      ) : (
        <View className='login__panel'>
          <View className='login__field'>
            <Input placeholder='账号' value={username} onChange={setUsername} />
          </View>
          <View className='login__field'>
            <Input type='password' placeholder='密码' value={password} onChange={setPassword} />
          </View>
          <View className='login__field login__field--captcha'>
            <Input placeholder='验证码' maxLength={8} value={captchaValue} onChange={setCaptchaValue} />
            <View className='login__captcha' onClick={refreshCaptcha}>
              {captcha ? (
                <Image className='login__captcha-img' src={captcha.imageBase64} mode='aspectFit' />
              ) : (
                <Text className='login__captcha-retry'>点击获取</Text>
              )}
            </View>
          </View>
          <Button type='primary' size='large' block loading={submitting} disabled={submitting} onClick={onPasswordLogin}>
            登录
          </Button>
        </View>
      )}

      {WECHAT_AVAILABLE && (
        <View className='login__switch' onClick={() => setMode(mode === 'wechat' ? 'password' : 'wechat')}>
          <Text className='login__switch-text'>{mode === 'wechat' ? '账号密码登录' : '微信一键登录'}</Text>
        </View>
      )}
    </View>
  )
}
