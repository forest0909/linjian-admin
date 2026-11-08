import { describe, expect, it } from 'vitest';

import {
  findActiveRootMenu,
  findFirstLeafPath,
  getLayoutShape,
} from '../src/layouts/MainLayout/utils/layoutMenu';

const menu = [
  { path: '/dashboard' },
  {
    path: '/system',
    children: [
      { path: '/system/user' },
      {
        path: '/system/permission',
        children: [{ path: '/system/permission/role' }],
      },
    ],
  },
  { path: '/opm', children: [{ path: '/opm/log' }] },
];

describe('findActiveRootMenu', () => {
  it('matches a root without children', () => {
    expect(findActiveRootMenu(menu, '/dashboard')?.path).toBe('/dashboard');
  });

  it('matches a deep link to its root', () => {
    expect(findActiveRootMenu(menu, '/system/permission/role')?.path).toBe('/system');
  });

  it('falls back to the path prefix for pages hidden from the menu', () => {
    expect(findActiveRootMenu(menu, '/opm/profile')?.path).toBe('/opm');
  });

  it('does not confuse a root with a sibling that shares its prefix', () => {
    expect(findActiveRootMenu(menu, '/systems/x')).toBeUndefined();
  });
});

describe('findFirstLeafPath', () => {
  it('returns the node itself when it has no children', () => {
    expect(findFirstLeafPath(menu[0])).toBe('/dashboard');
  });

  it('descends to the first leaf', () => {
    expect(findFirstLeafPath(menu[1])).toBe('/system/user');
    expect(
      findFirstLeafPath({ path: '/a', children: [{ path: '/a/b', children: [{ path: '/a/b/c' }] }] }),
    ).toBe('/a/b/c');
  });
});

describe('getLayoutShape', () => {
  it('keeps the plain sidebar layout with no extras', () => {
    expect(getLayoutShape('sidebar-nav', false)).toEqual({
      fullWidthHeader: false,
      headerFullMenu: false,
      headerRootMenu: false,
      menuRail: false,
      siderSubMenuOnly: false,
      sider: true,
      contentOnly: false,
    });
  });

  it('hides the sider for header-nav and full-content', () => {
    expect(getLayoutShape('header-nav', false).sider).toBe(false);
    expect(getLayoutShape('full-content', false).sider).toBe(false);
    expect(getLayoutShape('full-content', false).contentOnly).toBe(true);
  });

  it('shows only the sub menu in the sider for mixed-nav and sidebar-mixed-nav', () => {
    expect(getLayoutShape('mixed-nav', false).siderSubMenuOnly).toBe(true);
    expect(getLayoutShape('sidebar-mixed-nav', false).menuRail).toBe(true);
  });

  it('always uses the sidebar layout on mobile', () => {
    expect(getLayoutShape('header-nav', true)).toEqual(getLayoutShape('sidebar-nav', false));
    expect(getLayoutShape('full-content', true)).toEqual(getLayoutShape('sidebar-nav', false));
  });
});
