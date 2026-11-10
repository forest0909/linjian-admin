import { describe, expect, it } from 'vitest';
import { menuUpdateMask } from '../src/api/hooks/menu';
import { formValuesToMenu, menuToFormValues } from '../src/pages/app/permission/menu/menu-form';
import { transformRoutesToMenu } from '../src/core/router/utils/menu';
import type { AppRouteObject } from '../src/core/router/types';

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
