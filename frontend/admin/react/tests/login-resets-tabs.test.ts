import { beforeEach, describe, expect, it, vi } from 'vitest';

const { loginExecute, fetchUserProfile } = vi.hoisted(() => ({
  loginExecute: vi.fn(),
  fetchUserProfile: vi.fn(),
}));

vi.mock('@/api', () => ({
  fetchUserProfile,
  loginMutation: { execute: loginExecute },
  logoutMutation: { execute: vi.fn() },
  refreshTokenMutation: { execute: vi.fn() },
}));
vi.mock('@/utils', () => ({ encryptPassword: (value: string) => value }));
vi.mock('@/hooks/useTokenRefresh', () => ({
  startRefreshTimer: vi.fn(),
  stopRefreshTimer: vi.fn(),
  disconnectSSEServer: vi.fn(),
}));
vi.mock('@/core/query-client', () => ({ queryClient: { clear: vi.fn() } }));
vi.mock('@/core/transport/rest', () => ({ setCaptchaHeaders: vi.fn() }));
vi.mock('@/stores/user', () => ({
  useUserStore: { getState: () => ({ $reset: vi.fn() }) },
}));

import { useAuthStore } from '../src/stores/auth';
import { useTabsStore } from '../src/stores/tabs';

const staleTab = (path: string) => ({ key: path, path, title: path, closable: true });

describe('登录时重置页签', () => {
  beforeEach(() => {
    useAuthStore.setState({ accessToken: null, userInfo: null, error: null });
    useTabsStore.setState({ tabs: [staleTab('/opm/users'), staleTab('/log/login')] });
    fetchUserProfile.mockReset();
    fetchUserProfile.mockResolvedValue({ id: 1 });
    loginExecute.mockReset();
    loginExecute.mockResolvedValue({ access_token: 'token', expires_in: 7200 });
  });

  it('登录成功后清掉上次留下的页签', async () => {
    await useAuthStore.getState().login({ username: 'u', password: 'p' }, () => {});

    expect(useTabsStore.getState().tabs).toEqual([]);
  });

  it('写入 token 后加入的落地页页签不会被清掉', async () => {
    // token 一写 GuestGuard 就跳进主布局，TabsBar 随即加入当前页签
    const unsubscribe = useAuthStore.subscribe((state, prev) => {
      if (state.accessToken && !prev.accessToken) {
        useTabsStore.getState().addTab(staleTab('/analytics'));
      }
    });

    await useAuthStore.getState().login({ username: 'u', password: 'p' }, () => {});
    unsubscribe();

    expect(useTabsStore.getState().tabs.map((tab) => tab.path)).toEqual(['/analytics']);
  });

  it('登录失败时保留原有页签', async () => {
    loginExecute.mockRejectedValue(new Error('bad password'));

    await expect(
      useAuthStore.getState().login({ username: 'u', password: 'p' }, () => {}),
    ).rejects.toThrow('bad password');

    expect(useTabsStore.getState().tabs).toHaveLength(2);
  });
});
