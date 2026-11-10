import { expect, it } from 'vitest';
import { buildSyncMenusRequest } from '../src/api/hooks/menu';
import { businessRoutes } from '../src/router/business-routes';
import { createLazyRoute } from '../src/core/router';
import type { AppRouteObject } from '../src/core/router/types';
import { normalizeViewPath } from '../src/core/router/generators/generate-routes-backend';
import type { permissionservicev1_Menu as Menu } from '../src/api/generated/admin/service/v1';

const modules = import.meta.glob<{ default?: unknown }>('../src/pages/app/**/*.tsx', {
  eager: true,
});

it('maps the real MERGE sync payload into the backend pageMap', async () => {
  const pageMap = new Set(
    Object.entries(modules)
      .filter(([, mod]) => typeof mod.default === 'function')
      .map(([key]) => normalizeViewPath(key)),
  );
  const request = await buildSyncMenusRequest(businessRoutes, (key) => key);
  expect(request.mode).toBe('MERGE');
  let pages = 0;
  const hidden: string[] = [];
  const visit = (items: Menu[], parent = '') => {
    for (const menu of items) {
      const path = `${parent}/${menu.path}`.replace(/\/+/g, '/');
      if (menu.meta?.hideInMenu) hidden.push(path);
      if (menu.type === 'MENU') {
        pages++;
        expect(menu.component, menu.path).toBeTruthy();
        expect(pageMap.has(normalizeViewPath(menu.component ?? '')), menu.component).toBe(true);
      }
      visit(menu.children ?? [], path);
    }
  };
  visit(request.items ?? []);
  expect(pages).toBe(23);
  expect(hidden.sort()).toEqual([
    '/internal-message/inbox',
    '/opm/profile',
    '/opm/users/detail/:id',
    '/permission/codes',
  ]);
});

it('syncs one-level pages and nested catalogs with the right menu types', async () => {
  const page = () => createLazyRoute(() => import('../src/pages/app/opm/org-unit'));
  const routes: AppRouteObject[] = [
    { name: 'home', path: '/home', element: page(), meta: { title: 'home' } },
    {
      name: 'ops',
      path: 'ops',
      meta: { title: 'ops' },
      children: [
        {
          name: 'dispatch',
          path: 'dispatch',
          meta: { title: 'dispatch' },
          children: [{ name: 'plans', path: 'plans', element: page(), meta: { title: 'plans' } }],
        },
      ],
    },
  ];
  const { items } = await buildSyncMenusRequest(routes, (key) => key);
  const home = items?.find((m) => m.path === '/home');
  expect(home?.type).toBe('MENU');
  expect(home?.component).toBe('app/opm/org-unit/index.vue');
  const ops = items?.find((m) => m.path === '/ops');
  expect(ops?.type).toBe('CATALOG');
  expect(ops?.component).toBe('BasicLayout');
  const dispatch = ops?.children?.find((m) => m.path === 'dispatch');
  expect(dispatch?.type).toBe('CATALOG');
  expect(dispatch?.component).toBeUndefined();
  const plans = dispatch?.children?.find((m) => m.path === 'plans');
  expect(plans?.type).toBe('MENU');
  expect(plans?.component).toBe('app/opm/org-unit/index.vue');
});
