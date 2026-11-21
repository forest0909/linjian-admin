import { useEffect, useState } from 'react';
import { Alert, App, Button, Popconfirm, Space, Table, Typography, theme } from 'antd';
import { DisconnectOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import type { authenticationservicev1_MiniappBinding as MiniappBinding } from '@/api/generated/admin/service/v1';
import { useDeleteMiniappBinding, useListMiniappBindings } from '@/api/hooks/miniapp-binding';
import { useAccess } from '@/core/access';
import { formatDateTime } from '@/utils/date';

interface MiniappBindingCardProps {
  userId: number | undefined;
}

/**
 * 用户详情 - 微信小程序绑定卡片。
 * 解绑 = 删除这条绑定 + 吊销该用户全部小程序会话；绑定已不存在时仍返回成功。
 * 两个接口按后端自动生成的权限码控制：GET → user:view，DELETE → user:delete。
 */
const MiniappBindingCard: React.FC<MiniappBindingCardProps> = ({ userId }) => {
  const { t } = useTranslation('user-detail');
  const { token } = theme.useToken();
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const { hasAccessByCodes } = useAccess();
  const canUnbind = hasAccessByCodes(['user:delete']);

  // 解绑部分成功（绑定已删、吊销 app 令牌失败，接口 503）时，列表里已没有这一行，
  // 保留失败的操作，让管理员可以用同一个 ID 重试（接口幂等）。
  const [failedUnbind, setFailedUnbind] = useState<{ userId: number; id: number } | null>(null);

  const { data, isLoading, error } = useListMiniappBindings(userId ?? 0, {
    enabled: !!userId,
    retry: false,
  });

  useEffect(() => {
    if (error) {
      // 无权限（403）、接口未实现等：卡片内提示，不影响页面其余部分
      console.error('Failed to list miniapp bindings:', error);
    }
  }, [error]);

  const { mutate: unbind, isPending } = useDeleteMiniappBinding({
    onSuccess: () => {
      setFailedUnbind(null);
      message.success(t('miniapp.unbindSuccess'));
      queryClient.invalidateQueries({ queryKey: ['listMiniappBindings'] });
    },
    onError: (err: Error, vars) => {
      console.error('Failed to unbind miniapp:', err);
      message.error(err.message || t('miniapp.unbindFailed'));
      // 失败后不刷新列表：刷新可能让这一行消失，重试入口跟着没了
      setFailedUnbind(vars);
    },
  });

  const items = (data?.items ?? []) as MiniappBinding[];

  return (
    <div style={{ marginTop: 24 }}>
      <Typography.Title level={5} style={{ color: token.colorTextHeading }}>
        {t('miniapp.title')}
      </Typography.Title>
      {failedUnbind && canUnbind && (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 12 }}
          title={t('miniapp.retryHint')}
          action={
            <Space>
              <Button size="small" loading={isPending} onClick={() => unbind(failedUnbind)}>
                {t('miniapp.retry')}
              </Button>
              <Button
                size="small"
                type="text"
                onClick={() => {
                  setFailedUnbind(null);
                  queryClient.invalidateQueries({ queryKey: ['listMiniappBindings'] });
                }}
              >
                {t('miniapp.dismiss')}
              </Button>
            </Space>
          }
        />
      )}
      {error ? (
        <Alert type="warning" showIcon title={error.message || t('miniapp.loadFailed')} />
      ) : (
        <Table<MiniappBinding>
          size="small"
          rowKey="id"
          loading={isLoading}
          dataSource={items}
          pagination={false}
          locale={{ emptyText: t('miniapp.empty') }}
          columns={[
            { title: t('miniapp.openid'), dataIndex: 'openidMasked' },
            {
              title: t('miniapp.createdAt'),
              dataIndex: 'createdAt',
              render: (v) => formatDateTime(v),
            },
            ...(canUnbind
              ? [
                  {
                    title: t('miniapp.action'),
                    key: 'action',
                    width: 120,
                    render: (_: unknown, row: MiniappBinding) => (
                      <Popconfirm
                        title={t('miniapp.unbindConfirmTitle')}
                        description={t('miniapp.unbindConfirmDesc')}
                        okText={t('common:button.ok')}
                        cancelText={t('common:button.cancel')}
                        onConfirm={() => userId && row.id && unbind({ userId, id: row.id })}
                      >
                        <Button type="link" danger size="small" icon={<DisconnectOutlined />} loading={isPending}>
                          {t('miniapp.unbind')}
                        </Button>
                      </Popconfirm>
                    ),
                  },
                ]
              : []),
          ]}
        />
      )}
    </div>
  );
};

export default MiniappBindingCard;
