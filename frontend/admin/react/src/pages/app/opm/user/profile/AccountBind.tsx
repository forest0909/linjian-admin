import { useCallback, useEffect, useRef, useState } from 'react';
import { List, Button, Drawer, Form, Input, App, Alert } from 'antd';
import { MailOutlined, MobileOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import { apiClient } from '@/api/client';
import { useGetUserProfile } from '@/api/hooks/user-profile';
import { useDrawerCloseGuard } from '@/components/common/use-drawer-close-guard';

const ICON_STYLE = { fontSize: 40 };
// 重发冷却（秒）。服务端另有额度，超限的反馈照常展示
const RESEND_COOLDOWN = 60;

const maskEmail = (email: string) => {
  const at = email.lastIndexOf('@');
  if (at <= 0) return email;
  return `${email.slice(0, Math.min(2, at))}***${email.slice(at)}`;
};

const maskPhone = (phone: string) => {
  if (phone.length < 7) return phone;
  return `${phone.slice(0, 3)}****${phone.slice(-4)}`;
};

// 与后端号码规范一致：去首尾空格和 +86 前缀后必须是 1[3-9] 开头的 11 位数字
const normalizePhone = (raw: string) => raw.trim().replace(/^\+86/, '');
const PHONE_RE = /^1[3-9]\d{9}$/;

type BindKind = 'email' | 'phone';

const AccountBind = () => {
  const { t } = useTranslation(['profile', 'common']);
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const { data: user } = useGetUserProfile();

  const [bindOpen, setBindOpen] = useState(false);
  const [bindKind, setBindKind] = useState<BindKind>('email');
  // 发验证码返回 NOT_IMPLEMENTED：没配短信渠道，只能找管理员代改
  const [smsUnavailable, setSmsUnavailable] = useState(false);
  // 抽屉实例号：每次打开和关闭都加一。异步结果只在发起时的实例仍是当前实例时才改局部状态
  const instanceRef = useRef(0);
  const closeDrawer = useCallback(() => {
    instanceRef.current += 1;
    setBindOpen(false);
  }, []);
  // 关闭守卫：遮罩/ESC/X/取消统一，脏数据先确认（docs/form_drawer_close_guard.md）
  const { guardClose, handleValuesChange } = useDrawerCloseGuard(bindOpen, closeDrawer);
  const [bindForm] = Form.useForm();
  const codeValue = Form.useWatch('code', bindForm);
  // 验证码实际发往的收件地址（规范化后）；收件地址改了，验证码作废
  const [sentTarget, setSentTarget] = useState<string | null>(null);
  const [cooldown, setCooldown] = useState(0);
  const [sending, setSending] = useState(false);
  const [verifying, setVerifying] = useState(false);

  useEffect(() => {
    if (cooldown <= 0) return undefined;
    const timer = setTimeout(() => setCooldown((c) => c - 1), 1000);
    return () => clearTimeout(timer);
  }, [cooldown]);

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
    instanceRef.current += 1;
    setBindKind(kind);
    bindForm.resetFields();
    bindForm.setFieldsValue(kind === 'email' ? { email: user?.email ?? '' } : { phone: '' });
    setSentTarget(null);
    setCooldown(0);
    setSmsUnavailable(false);
    setSending(false);
    setVerifying(false);
    setBindOpen(true);
  };

  const targetField = bindKind === 'phone' ? 'phone' : 'email';
  const normalizeTarget = (raw: unknown) =>
    bindKind === 'phone' ? normalizePhone(String(raw ?? '')) : String(raw ?? '').trim();

  // 收件地址改了：之前发出的验证码对新地址无效，清掉输入并允许立即重发
  const handleFormValuesChange = (changed: Record<string, unknown>) => {
    handleValuesChange();
    if (targetField in changed && sentTarget !== null && normalizeTarget(changed[targetField]) !== sentTarget) {
      bindForm.setFieldValue('code', '');
      setSentTarget(null);
      setCooldown(0);
    }
  };

  const handleSendCode = async () => {
    let values: Record<string, string>;
    try {
      values = await bindForm.validateFields([targetField]);
    } catch (error) {
      // 字段校验失败：antd 已就地标红，这里只留日志
      console.error('Bind contact form validation failed:', error);
      return;
    }
    const instance = instanceRef.current;
    const isPhone = bindKind === 'phone';
    const target = normalizeTarget(values[targetField]);
    setSending(true);
    try {
      if (isPhone) {
        await apiClient.userProfileService.BindContact({ phone: { phone: target, code: undefined } });
      } else {
        await apiClient.userProfileService.BindContact({ email: { email: target } });
      }
      if (instance !== instanceRef.current) return;
      setSentTarget(target);
      setCooldown(RESEND_COOLDOWN);
      message.success(t('accountBind.codeSent'));
    } catch (error: any) {
      console.error('Failed to send bind code:', error);
      if (instance !== instanceRef.current) return;
      if (isPhone && error?.reason === 'NOT_IMPLEMENTED') {
        setSmsUnavailable(true);
      } else {
        // 含服务端限流反馈
        message.error(error?.message || t('accountBind.vcodeSendFailed'));
      }
    } finally {
      if (instance === instanceRef.current) setSending(false);
    }
  };

  const handleFinish = async (values: Record<string, string>) => {
    const instance = instanceRef.current;
    const isPhone = bindKind === 'phone';
    const target = normalizeTarget(values[targetField]);
    const code = values.code?.trim();
    const oldMobile = user?.mobile ?? '';
    if (isPhone && target === oldMobile) {
      // 与库里存的值完全一致：后端不会有任何改动。库里是带区号/空格的旧格式时，
      // 规范值和存储值不同，仍可提交，用来把它清洗成标准格式
      message.info(t('accountBind.phoneUnchanged'));
      return;
    }
    if (sentTarget !== target) {
      message.error(t('accountBind.vcodeRequired'));
      return;
    }
    setVerifying(true);
    try {
      if (isPhone) {
        await apiClient.userProfileService.VerifyContact({ phone: { phone: target, code } });
      } else {
        await apiClient.userProfileService.VerifyContact({ email: { email: target, code } });
      }
      // 后台已写入：无论抽屉还在不在，都刷新资料（本页和顶栏头像都读 getMe）
      await queryClient.refetchQueries({ queryKey: ['getMe'] });
      if (instance !== instanceRef.current) return;
      message.success(isPhone ? t('accountBind.phoneBindSuccess') : t('accountBind.bindSuccess'));
      closeDrawer();
    } catch (error: any) {
      console.error('Failed to verify bind code:', error);
      // 改号先落库再撤销会话：Redis 失败时接口返回 503，但号码可能已经改了。回读判断
      await queryClient.refetchQueries({ queryKey: ['getMe'] }).catch((e) => {
        console.error('Failed to refresh profile after bind error:', e);
      });
      if (instance !== instanceRef.current) return;
      const latest = queryClient.getQueryData<{ mobile?: string }>(['getMe']);
      if (isPhone && latest?.mobile === target && target !== oldMobile) {
        message.warning(t('accountBind.phonePartial'), 8);
        closeDrawer();
      } else {
        message.error(error?.message || t('accountBind.bindFailed'));
      }
    } finally {
      if (instance === instanceRef.current) setVerifying(false);
    }
  };

  const phoneKind = bindKind === 'phone';
  const resendDisabled = sending || cooldown > 0 || smsUnavailable;

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
            <List.Item.Meta avatar={item.icon} title={item.title} description={item.description} />
          </List.Item>
        )}
      />

      <Drawer
        title={phoneKind ? t('accountBind.bindPhoneTitle') : t('accountBind.bindEmailTitle')}
        open={bindOpen}
        onClose={guardClose}
        size={480}
        destroyOnHidden
        footer={
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
            <Button onClick={guardClose}>{t('common:button.cancel')}</Button>
            <Button type="primary" loading={verifying} disabled={smsUnavailable} onClick={() => bindForm.submit()}>
              {t('common:button.ok')}
            </Button>
          </div>
        }
      >
        <Form
          form={bindForm}
          layout="horizontal"
          labelCol={{ flex: '0 0 120px' }}
          wrapperCol={{ flex: 'auto' }}
          onValuesChange={handleFormValuesChange}
          onFinish={handleFinish}
        >
          {phoneKind ? (
            <>
              <Alert type="info" showIcon style={{ marginBottom: 16 }} title={t('accountBind.phoneHint')} />
              {smsUnavailable && (
                <Alert type="warning" showIcon style={{ marginBottom: 16 }} title={t('accountBind.smsNotConfigured')} />
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
              <Input
                placeholder={phoneKind ? t('accountBind.phoneVcodePlaceholder') : t('accountBind.vcodePlaceholder')}
                value={codeValue ?? ''}
                onChange={(e) => {
                  handleValuesChange();
                  bindForm.setFieldValue('code', e.target.value);
                }}
              />
              <Button disabled={resendDisabled} loading={sending} onClick={handleSendCode}>
                {cooldown > 0
                  ? t('accountBind.resendIn', { seconds: cooldown })
                  : sentTarget !== null
                    ? t('accountBind.resend')
                    : t('accountBind.sendVcode')}
              </Button>
            </div>
          </Form.Item>
        </Form>
      </Drawer>
    </>
  );
};

export default AccountBind;
