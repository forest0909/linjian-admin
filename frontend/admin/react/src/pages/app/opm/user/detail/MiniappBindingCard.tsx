import { Alert, App, Button, Popconfirm, Table, Typography, theme } from 'antd';
import { useTranslation } from 'react-i18next';
import { useQueryClient } from '@tanstack/react-query';
import type { authenticationservicev1_MiniappBinding as MiniappBinding } from '@/api/generated/admin/service/v1';
import { useDeleteMiniappBinding, useListMiniappBindings } from '@/api/hooks/miniapp-binding';
import { formatDateTime } from '@/utils/date';

interface MiniappBindingCardProps {
  userId: number | undefined;
}

/**
 * 用户详情 - 微信小程序绑定卡片。
 * 解绑 = 删除这条绑定 + 吊销该用户全部小程序会话；绑定已不存在时仍返回成功。
 */
const MiniappBindingCard: React.FC<MiniappBindingCardProps> = ({ userId }) => {
  const { t } = useTranslation('user-detail');
  const { token } = theme.useToken();
  const { message } = App.useApp();
  const queryClient = useQueryClient();

  const { data, isLoading, error } = useListMiniappBindings(userId ?? 0, {
    enabled: !!userId,
    retry: false,
  });

  const { mutate: unbind, isPending } = useDeleteMiniappBinding({
    onSuccess: () => {
      message.success(t('miniapp.unbindSuccess'));
      queryClient.invalidateQueries({ queryKey: ['listMiniappBindings'] });
    },
    onError: (err: Error) => {
      console.error('Failed to unbind miniapp:', err);
      message.error(err.message || t('miniapp.unbindFailed'));
      // 失败后也刷新：绑定可能已被别处删掉
      queryClient.invalidateQueries({ queryKey: ['listMiniappBindings'] });
    },
  });

  if (error) {
    // 无权限（403）、接口未实现等：只在卡片内提示，不影响页面其余部分，也不渲染解绑按钮
    console.error('Failed to list miniapp bindings:', error);
  }

  const items = (data?.items ?? []) as MiniappBinding[];

  return (
    <div style={{ marginTop: 24 }}>
      <Typography.Title level={5} style={{ color: token.colorTextHeading }}>
        {t('miniapp.title')}
      </Typography.Title>
      {error ? (
        <Alert type="warning" showIcon message={error.message || t('miniapp.loadFailed')} />
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
            {
              title: t('miniapp.action'),
              key: 'action',
              width: 100,
              render: (_, row) => (
                <Popconfirm
                  title={t('miniapp.unbindConfirmTitle')}
                  description={t('miniapp.unbindConfirmDesc')}
                  okText={t('common:button.ok')}
                  cancelText={t('common:button.cancel')}
                  onConfirm={() => userId && row.id && unbind({ userId, id: row.id })}
                >
                  <Button type="link" danger size="small" loading={isPending}>
                    {t('miniapp.unbind')}
                  </Button>
                </Popconfirm>
              ),
            },
          ]}
        />
      )}
    </div>
  );
};

export default MiniappBindingCard;
