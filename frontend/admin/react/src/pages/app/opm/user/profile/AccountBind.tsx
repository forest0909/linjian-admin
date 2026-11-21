import { useState } from 'react';
import { List, Button, Drawer, Form, Input, App, Alert } from 'antd';
import { MailOutlined, MobileOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { apiClient } from '@/api/client';
import { useGetUserProfile } from '@/api/hooks/user-profile';
import { useDrawerCloseGuard } from '@/components/common/use-drawer-close-guard';

const ICON_STYLE = { fontSize: 40 };

// 与后端号码规范一致：去首尾空格和 +86 前缀后必须是 1[3-9] 开头的 11 位数字
const normalizePhone = (raw: string) => raw.trim().replace(/^\+86/, '');
const PHONE_RE = /^1[3-9]\d{9}$/;

type BindKind = 'email' | 'phone';

const maskEmail = (email: string) => {
  const at = email.lastIndexOf('@');
  if (at <= 0) return email;
  return `${email.slice(0, Math.min(2, at))}***${email.slice(at)}`;
};

const maskPhone = (phone: string) => {
  if (phone.length < 7) return phone;
  return `${phone.slice(0, 3)}****${phone.slice(-4)}`;
};

const AccountBind = () => {
  const { t } = useTranslation(['profile', 'common']);
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const { data: user } = useGetUserProfile();

  const [bindOpen, setBindOpen] = useState(false);
  const [bindKind, setBindKind] = useState<BindKind>('email');
  // 发验证码返回 NOT_IMPLEMENTED：没配短信渠道，只能找管理员代改
  const [smsUnavailable, setSmsUnavailable] = useState(false);
  // 关闭守卫：遮罩/ESC/X/取消统一，脏数据先确认（docs/form_drawer_close_guard.md）
  const { guardClose, handleValuesChange } = useDrawerCloseGuard(bindOpen, () => setBindOpen(false));
  const [bindForm] = Form.useForm();
  const [codeSent, setCodeSent] = useState(false);
  const [sending, setSending] = useState(false);
  const [verifying, setVerifying] = useState(false);

  const bindList = [
    {
      key: 'email',
      title: t('accountBind.email'),
      description: user?.email
        ? t('accountBind.bound', { value: maskEmail(user.email) })
        : t('accountBind.unbound'),
      icon: <MailOutlined style={ICON_STYLE} />,
    },
    {
      key: 'phone',
      title: t('accountBind.phone'),
      description: user?.mobile
        ? t('accountBind.bound', { value: maskPhone(user.mobile) })
        : t('accountBind.phoneUnbound'),
      icon: <MobileOutlined style={ICON_STYLE} />,
    },
  ];

  const openBind = (kind: BindKind) => {
    setBindKind(kind);
    bindForm.resetFields();
    bindForm.setFieldsValue(kind === 'email' ? { email: user?.email ?? '' } : { phone: '' });
    setCodeSent(false);
    setSmsUnavailable(false);
    setBindOpen(true);
  };

  const handleSendCode = async () => {
    const isPhone = bindKind === 'phone';
    const values = await bindForm.validateFields([isPhone ? 'phone' : 'email']);
    setSending(true);
    try {
      if (isPhone) {
        await apiClient.userProfileService.BindContact({
          phone: { phone: normalizePhone(values.phone), code: undefined },
        });
      } else {
        const email = values.email?.trim();
        if (!email) {
          message.error(t('accountBind.newEmailRequired'));
          return;
        }
        await apiClient.userProfileService.BindContact({ email: { email } });
      }
      setCodeSent(true);
      message.success(t('accountBind.codeSent'));
    } catch (error: any) {
      console.error('Failed to send bind code:', error);
      if (isPhone && error?.reason === 'NOT_IMPLEMENTED') {
        setSmsUnavailable(true);
      } else {
        message.error(error?.message || t('accountBind.vcodeSendFailed'));
      }
    } finally {
      setSending(false);
    }
  };

  const handleVerify = async () => {
    const isPhone = bindKind === 'phone';
    const values = await bindForm.validateFields();
    const code = values.code?.trim();
    if (!code) {
      message.error(t('accountBind.vcodeRequired'));
      return;
    }
    setVerifying(true);
    try {
      if (isPhone) {
        await apiClient.userProfileService.VerifyContact({
          phone: { phone: normalizePhone(values.phone), code },
        });
      } else {
        await apiClient.userProfileService.VerifyContact({
          email: { email: values.email?.trim(), code },
        });
      }
      message.success(isPhone ? t('accountBind.phoneBindSuccess') : t('accountBind.bindSuccess'));
      setBindOpen(false);
      // 刷新用户信息以更新绑定状态展示（本页读 getMe，顶栏头像等也读 getMe）
      queryClient.invalidateQueries({ queryKey: ['getMe'] });
    } catch (error: any) {
      console.error('Failed to verify bind code:', error);
      message.error(error?.message || t('accountBind.bindFailed'));
    } finally {
      setVerifying(false);
    }
  };

  return (
    <>
      <List
        dataSource={bindList}
        renderItem={(item) => (
          <List.Item
            actions={[
              item.key === 'email' ? (
                <Button type="link" size="small" onClick={() => openBind('email')}>
                  {user?.email ? t('accountBind.rebind') : t('accountBind.bindNow')}
                </Button>
              ) : (
                <Button type="link" size="small" onClick={() => openBind('phone')}>
                  {user?.mobile ? t('accountBind.phoneRebind') : t('accountBind.phoneBindNow')}
                </Button>
              ),
            ]}
          >
            <List.Item.Meta
              avatar={item.icon}
              title={item.title}
              description={item.description}
            />
          </List.Item>
        )}
      />

      <Drawer
        title={bindKind === 'phone' ? t('accountBind.bindPhoneTitle') : t('accountBind.bindEmailTitle')}
        open={bindOpen}
        onClose={guardClose}
        width={480}
        destroyOnClose
        footer={
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
            <Button onClick={guardClose}>{t('common:button.cancel')}</Button>
            <Button type="primary" loading={verifying} disabled={smsUnavailable} onClick={handleVerify}>
              {t('common:button.ok')}
            </Button>
          </div>
        }
      >
        <Form form={bindForm} layout="horizontal" labelCol={{ flex: '0 0 120px' }} wrapperCol={{ flex: 'auto' }} onValuesChange={handleValuesChange}>
          {bindKind === 'phone' ? (
            <>
              <Alert type="info" showIcon style={{ marginBottom: 16 }} message={t('accountBind.phoneHint')} />
              {smsUnavailable && (
                <Alert type="warning" showIcon style={{ marginBottom: 16 }} message={t('accountBind.smsNotConfigured')} />
              )}
              <Form.Item
                name="phone"
                label={t('accountBind.newPhone')}
                rules={[
                  { required: true, message: t('accountBind.newPhoneRequired') },
                  {
                    validator: (_, value) =>
                      !value || PHONE_RE.test(normalizePhone(String(value)))
                        ? Promise.resolve()
                        : Promise.reject(new Error(t('accountBind.newPhoneInvalid'))),
                  },
                ]}
              >
                <Input placeholder={t('accountBind.newPhonePlaceholder')} />
              </Form.Item>
            </>
          ) : (
            <Form.Item
              name="email"
              label={t('accountBind.newEmail')}
              rules={[{ required: true, message: t('accountBind.newEmailRequired') }]}
            >
              <Input placeholder={t('accountBind.newEmailPlaceholder')} />
            </Form.Item>
          )}
          <Form.Item
            name="code"
            label={t('accountBind.vcode')}
            rules={[{ required: true, message: t('accountBind.vcodeRequired') }]}
          >
            <div style={{ display: 'flex', gap: 8 }}>
              <Input placeholder={bindKind === 'phone' ? t('accountBind.phoneVcodePlaceholder') : t('accountBind.vcodePlaceholder')} />
              <Button disabled={codeSent || smsUnavailable} loading={sending} onClick={handleSendCode}>
                {codeSent ? t('accountBind.codeSent') : t('accountBind.sendVcode')}
              </Button>
            </div>
          </Form.Item>
        </Form>
      </Drawer>
    </>
  );
};

export default AccountBind;
