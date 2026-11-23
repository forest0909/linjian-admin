import { describe, expect, it } from 'vitest';
import { buildSyncMenusRequest, menuUpdateMask } from '../src/api/hooks/menu';
import { formValuesToMenu, menuToFormValues } from '../src/pages/app/permission/menu/menu-form';
import { transformRoutesToMenu } from '../src/core/router/utils/menu';
import type { AppRouteObject } from '../src/core/router/types';
import { resolveMenuKey } from '../src/layouts/MainLayout/utils/layoutMenu';

describe('menu drawer form', () => {
  it('echoes the four hide flags and activePath from the saved menu', () => {
    const values = menuToFormValues({
      id: 50,
      type: 'MENU',
      path: 'detail/:id',
      meta: {
        title: 't',
        hideInMenu: true,
        hideChildrenInMenu: true,
        hideInBreadcrumb: true,
        hideInTab: true,
        activePath: '/map',
      },
    });
    expect(values).toMatchObject({
      'meta.hideInMenu': true,
      'meta.hideChildrenInMenu': true,
      'meta.hideInBreadcrumb': true,
      'meta.hideInTab': true,
      'meta.activePath': '/map',
    });
  });

  it('submits only the fields the drawer rendered for a catalog', () => {
    // 目录不渲染组件路径、权限标识、KeepAlive、固定标签页、激活菜单
    const payload = formValuesToMenu({
      type: 'CATALOG',
      status: 'ON',
      path: '/log',
      parentId: undefined,
      'meta.title': '日志审计',
      'meta.order': 2004,
      'meta.icon': 'lucide:logs',
      'meta.hideInMenu': true,
      'meta.hideChildrenInMenu': false,
      'meta.hideInBreadcrumb': false,
      'meta.hideInTab': false,
    });
    expect(payload).toEqual({
      type: 'CATALOG',
      status: 'ON',
      path: '/log',
      parentId: 0,
      meta: {
        title: '日志审计',
        order: 2004,
        icon: 'lucide:logs',
        hideInMenu: true,
        hideChildrenInMenu: false,
        hideInBreadcrumb: false,
        hideInTab: false,
      },
    });
    expect(menuUpdateMask(payload).split(',').sort()).toEqual(
      [
        'type',
        'status',
        'path',
        'parentId',
        'meta.title',
        'meta.order',
        'meta.icon',
        'meta.hideInMenu',
        'meta.hideChildrenInMenu',
        'meta.hideInBreadcrumb',
        'meta.hideInTab',
        'id',
      ].sort(),
    );
  });

  it('rewrites the old page component when a top-level menu becomes a catalog', () => {
    // 类型切成目录后组件路径输入框卸载，values 里没有 component
    const payload = formValuesToMenu(
      { type: 'CATALOG', status: 'ON', path: '/group', parentId: undefined, 'meta.title': 'g' },
      'MENU',
    );
    expect(payload.component).toBe('BasicLayout');
    expect(menuUpdateMask(payload).split(',')).toContain('component');
  });

  it('clears the old page component when a nested menu becomes a catalog', () => {
    const payload = formValuesToMenu(
      { type: 'CATALOG', status: 'ON', path: 'group', parentId: 10300, 'meta.title': 'g' },
      'MENU',
    );
    expect(payload).toHaveProperty('component', undefined);
    expect(menuUpdateMask(payload).split(',')).toContain('component');
    expect(JSON.stringify(payload)).not.toContain('component');
  });

  it('leaves the component alone when a catalog stays a catalog', () => {
    const payload = formValuesToMenu(
      { type: 'CATALOG', status: 'ON', path: '/log', parentId: undefined, 'meta.title': 'g' },
      'CATALOG',
    );
    expect(payload).not.toHaveProperty('component');
    expect(menuUpdateMask(payload).split(',')).not.toContain('component');
  });

  it('sends unchecked switches as explicit false so a menu sync cannot fill them back', () => {
    const payload = formValuesToMenu({
      type: 'MENU',
      'meta.keepAlive': undefined,
      'meta.affixTab': false,
      'meta.hideInMenu': false,
      'meta.hideChildrenInMenu': undefined,
      'meta.hideInBreadcrumb': false,
      'meta.hideInTab': false,
    });
    expect(payload.meta).toEqual({
      keepAlive: false,
      affixTab: false,
      hideInMenu: false,
      hideChildrenInMenu: false,
      hideInBreadcrumb: false,
      hideInTab: false,
    });
  });

  it('clears activePath by listing it in the mask without a value', () => {
    const payload = formValuesToMenu({ type: 'MENU', 'meta.activePath': '  ' });
    expect(payload.meta).toHaveProperty('activePath', undefined);
    expect(menuUpdateMask(payload)).toContain('meta.activePath');
    expect(JSON.stringify(payload)).not.toContain('activePath');
  });

  it('never sends the whole meta as one mask path', () => {
    expect(menuUpdateMask({ meta: { hideInMenu: true } }).split(',')).not.toContain('meta');
  });
});

describe('sidebar menu', () => {
  const routes: AppRouteObject[] = [
    {
      path: '/log',
      meta: { title: 'log', hideChildrenInMenu: true },
      children: [{ path: 'login', meta: { title: 'login' } }],
    },
    { path: '/system', meta: { title: 'system' }, children: [{ path: 'dict', meta: { title: 'dict' } }] },
    { path: '/hidden', meta: { title: 'hidden', hideInMenu: true } },
  ];

  it('keeps a catalog with hideChildrenInMenu but drops its children', () => {
    const menus = transformRoutesToMenu(routes, [], { checkAuthority: false }) ?? [];
    expect(menus.map((m) => m.path)).toEqual(['/log', '/system']);
    expect(menus[0].children).toBeUndefined();
    expect(menus[1].children?.map((m) => m.path)).toEqual(['/system/dict']);
  });
});

describe('sidebar highlight under hideChildrenInMenu', () => {
  // 三级：/ops → dispatch（隐藏子菜单）→ plans、orders；/log（隐藏子菜单）→ login
  const routes: AppRouteObject[] = [
    {
      path: '/ops',
      meta: { title: 'ops' },
      children: [
        {
          path: 'dispatch',
          meta: { title: 'dispatch', hideChildrenInMenu: true },
          children: [
            { path: 'plans', meta: { title: 'plans' } },
            { path: 'orders', meta: { title: 'orders' } },
          ],
        },
        { path: 'patrol', meta: { title: 'patrol' } },
      ],
    },
    {
      path: '/log',
      meta: { title: 'log', hideChildrenInMenu: true },
      children: [{ path: 'login', meta: { title: 'login' } }],
    },
    { path: '/map', meta: { title: 'map' } },
  ];
  const menus = transformRoutesToMenu(routes, [], { checkAuthority: false }) ?? [];

  it('maps pages under a top-level catalog to the catalog', () => {
    expect(resolveMenuKey(menus, '/log/login')).toBe('/log');
  });

  it('maps every page under a nested catalog to that catalog, not its first child', () => {
    expect(menus[0].children?.map((m) => m.path)).toEqual(['/ops/dispatch', '/ops/patrol']);
    expect(resolveMenuKey(menus, '/ops/dispatch/plans')).toBe('/ops/dispatch');
    expect(resolveMenuKey(menus, '/ops/dispatch/orders')).toBe('/ops/dispatch');
  });

  it('keeps exact matches and unrelated paths as they are', () => {
    expect(resolveMenuKey(menus, '/ops/patrol')).toBe('/ops/patrol');
    expect(resolveMenuKey(menus, '/log')).toBe('/log');
    expect(resolveMenuKey(menus, '/map/reservoir/1')).toBe('/map/reservoir/1');
    expect(resolveMenuKey(menus, '/ops/patrolx')).toBe('/ops/patrolx');
  });
});

describe('menu sync payload', () => {
  it('carries activePath and hideInBreadcrumb so a sync does not wipe them', async () => {
    const request = await buildSyncMenusRequest(
      [
        {
          path: '/map',
          name: 'Map',
          meta: { title: 'map' },
          children: [
            {
              path: 'reservoir/:id',
              name: 'Detail',
              meta: { title: 'detail', hideInMenu: true, hideInBreadcrumb: true, activePath: '/map' },
            },
          ],
        },
      ],
      (key) => key,
    );
    expect(request.items?.[0].children?.[0].meta).toMatchObject({
      activePath: '/map',
      hideInBreadcrumb: true,
      hideInMenu: true,
    });
  });
});
