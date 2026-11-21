import { useState } from 'react'
import { Text, View } from '@tarojs/components'
import { Avatar, Button, Cell, Input, Popup } from '@nutui/nutui-react-taro'
import Taro from '@tarojs/taro'
import { logout } from '@/api/auth'
import { avatarSrc, changePassword, loadUser, uploadAvatar } from '@/api/profile'
import { ApiError } from '@/core/errors'
import { useRequireLogin } from '@/core/guard'
import { clearSession, toLoginPage, useSession } from '@/core/session'
import { MAX_AVATAR_BYTES, pickImage } from './avatar'
import './index.scss'

function toast(title: string) {
  Taro.showToast({ title, icon: 'none' })
}

/** 手机号只读展示，中间四位打码。后端已经打过码的原样显示。 */
function maskMobile(mobile: string | undefined): string {
  if (!mobile) {
    return '未设置'
  }
  return /^\d{11}$/.test(mobile) ? `${mobile.slice(0, 3)}****${mobile.slice(7)}` : mobile
}

export default function Mine() {
  useRequireLogin()
  const user = useSession((s) => s.user)

  const [uploading, setUploading] = useState(false)
  const [pwdVisible, setPwdVisible] = useState(false)
  const [oldPassword, setOldPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [saving, setSaving] = useState(false)
  const [leaving, setLeaving] = useState(false)

  const displayName = user?.nickname || user?.realname || user?.username || ''

  async function onChangeAvatar() {
    if (uploading) {
      return
    }
    setUploading(true)
    try {
      const image = await pickImage()
      if (!image) {
        return
      }
      if (image.size > MAX_AVATAR_BYTES) {
        toast('图片不能超过 5MB')
        return
      }
      await uploadAvatar(image.base64)
      await loadUser()
      toast('头像已更新')
    } catch (err) {
      console.error('[mine] 更换头像失败', err)
      if (!(err instanceof ApiError)) {
        toast('读取图片失败，请重试')
      }
    } finally {
      setUploading(false)
    }
  }

  function closePassword() {
    setPwdVisible(false)
    setOldPassword('')
    setNewPassword('')
    setConfirmPassword('')
  }

  async function onSavePassword() {
    if (!oldPassword || !newPassword) {
      toast('请输入原密码和新密码')
      return
    }
    if (newPassword !== confirmPassword) {
      toast('两次输入的新密码不一致')
      return
    }
    setSaving(true)
    try {
      await changePassword(oldPassword, newPassword)
    } catch (err) {
      console.error('[mine] 修改密码失败', err)
      setSaving(false)
      return
    }
    // 改密后服务端会让这个账号的小程序会话和微信绑定全部失效，直接回登录页重新登录。
    clearSession()
    toLoginPage()
    toast('密码已修改，请重新登录')
  }

  async function onLogout() {
    setLeaving(true)
    try {
      await logout()
    } finally {
      setLeaving(false)
    }
  }

  return (
    <View className='mine'>
      <View className='mine__header' onClick={onChangeAvatar}>
        <Avatar size='64' src={avatarSrc(user?.avatar)} background='#006be6' color='#ffffff'>
          {displayName.slice(0, 1)}
        </Avatar>
        <View className='mine__header-text'>
          <Text className='mine__name'>{displayName || '未登录'}</Text>
          <Text className='mine__tip'>{uploading ? '正在上传…' : '点击更换头像'}</Text>
        </View>
      </View>

      <View className='mine__group'>
        <Cell.Group>
          <Cell title='昵称' extra={user?.nickname || '未设置'} />
          <Cell title='用户名' extra={user?.username || ''} />
          <Cell title='手机号' extra={maskMobile(user?.mobile)} />
        </Cell.Group>
      </View>

      <View className='mine__group'>
        <Cell.Group>
          <Cell title='修改密码' extra='›' clickable onClick={() => setPwdVisible(true)} />
        </Cell.Group>
      </View>

      <View className='mine__logout'>
        <Button block size='large' fill='outline' type='danger' loading={leaving} disabled={leaving} onClick={onLogout}>
          退出登录
        </Button>
      </View>

      <Popup visible={pwdVisible} position='bottom' round closeable title='修改密码' onClose={closePassword}>
        <View className='mine__pwd'>
          <View className='mine__field'>
            <Input type='password' placeholder='原密码' value={oldPassword} onChange={setOldPassword} />
          </View>
          <View className='mine__field'>
            <Input type='password' placeholder='新密码' value={newPassword} onChange={setNewPassword} />
          </View>
          <View className='mine__field'>
            <Input type='password' placeholder='再输入一次新密码' value={confirmPassword} onChange={setConfirmPassword} />
          </View>
          <Text className='mine__pwd-tip'>修改成功后需要重新登录</Text>
          <Button block size='large' type='primary' loading={saving} disabled={saving} onClick={onSavePassword}>
            保存
          </Button>
        </View>
      </Popup>
    </View>
  )
}
