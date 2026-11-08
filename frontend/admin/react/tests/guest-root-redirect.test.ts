import { act, createElement } from 'react';
import { createRoot } from 'react-dom/client';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';

const auth = vi.hoisted(() => ({ accessToken: null as string | null }));

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: typeof auth) => unknown) => selector(auth),
}));

import { GuestRootRedirect } from '../src/router/guards/GuestRootRedirect';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const renderRootRoute = async () => {
  const router = createMemoryRouter(
    [
      { path: '/', element: createElement(GuestRootRedirect) },
      { path: '/auth/login', element: null },
    ],
    { initialEntries: ['/'] },
  );
  const root = createRoot(document.createElement('div'));
  await act(async () => root.render(createElement(RouterProvider, { router })));
  return { location: router.state.location, unmount: () => act(async () => root.unmount()) };
};

describe('GuestRootRedirect', () => {
  afterEach(() => {
    auth.accessToken = null;
  });

  it('未登录打开 / 时去登录页，不带 redirect', async () => {
    const { location, unmount } = await renderRootRoute();

    expect(location.pathname).toBe('/auth/login');
    expect(location.search).toBe('');
    await unmount();
  });

  it('刚登录成功时停在 /，等新路由表解析首个菜单', async () => {
    auth.accessToken = 'token';

    const { location, unmount } = await renderRootRoute();

    expect(location.pathname).toBe('/');
    await unmount();
  });
});
