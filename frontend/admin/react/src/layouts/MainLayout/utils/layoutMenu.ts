import type { LayoutType } from '@/core/preferences/types';

/** useMenuData 输出的菜单节点：path 已是绝对路径，空 children 已归一为 undefined */
export interface LayoutMenuNode {
  path?: string;
  name?: string;
  icon?: string;
  /** 目录开了隐藏子菜单：子项已从 children 里拿掉，子页面归到这一项 */
  hideChildrenInMenu?: boolean;
  children?: LayoutMenuNode[];
}

const containsPath = (node: LayoutMenuNode, pathname: string): boolean =>
  node.path === pathname || (node.children ?? []).some((child) => containsPath(child, pathname));

/**
 * 由当前路径算出激活的一级菜单。
 * 先在菜单树里找精确匹配；找不到（路径是隐藏在菜单外的详情页）时按路径前缀归属，取最长前缀。
 */
export const findActiveRootMenu = (
  menuData: LayoutMenuNode[],
  pathname: string,
): LayoutMenuNode | undefined => {
  const exact = menuData.find((root) => containsPath(root, pathname));
  if (exact) return exact;

  return menuData
    .filter((root) => root.path && pathname.startsWith(`${root.path}/`))
    .sort((a, b) => (b.path?.length ?? 0) - (a.path?.length ?? 0))[0];
};

/**
 * 当前页（或 activePath）在侧栏里对应哪一项。
 * 菜单里有这条路径就是它自己；没有时，如果它落在某个隐藏了子菜单的目录下面，归到这个目录（取最深的一个）。
 * 都不是就原样返回，保持以前的行为。
 */
export const resolveMenuKey = (menuData: LayoutMenuNode[], target: string): string => {
  let owner: string | undefined;
  const walk = (nodes: LayoutMenuNode[]): boolean => {
    for (const node of nodes) {
      if (node.path === target) return true;
      if (
        node.hideChildrenInMenu &&
        node.path &&
        target.startsWith(`${node.path}/`) &&
        node.path.length > (owner?.length ?? 0)
      ) {
        owner = node.path;
      }
      if (node.children && walk(node.children)) return true;
    }
    return false;
  };
  if (walk(menuData)) return target;
  return owner ?? target;
};

/** 点击一级菜单时的跳转目标：第一个叶子，避免停在没有页面的目录上 */
export const findFirstLeafPath = (node: LayoutMenuNode): string | undefined => {
  const [firstChild] = node.children ?? [];
  return firstChild ? findFirstLeafPath(firstChild) : node.path;
};

export interface LayoutShape {
  /** 顶栏横跨整个视口（水平、混合菜单） */
  fullWidthHeader: boolean;
  /** 顶栏里放完整层级的水平菜单（水平） */
  headerFullMenu: boolean;
  /** 顶栏里只放一级菜单，子菜单进侧栏（混合菜单） */
  headerRootMenu: boolean;
  /** 最左侧一级菜单图标栏（双列菜单） */
  menuRail: boolean;
  /** 侧栏只放当前一级的子菜单（混合菜单、双列菜单） */
  siderSubMenuOnly: boolean;
  /** 显示侧栏（水平、内容全屏不显示） */
  sider: boolean;
  /** 顶栏、页签、页脚全部隐藏（内容全屏） */
  contentOnly: boolean;
}

/** 手机宽度一律走垂直布局，由 SiderMenu 现有的隐藏逻辑兜底；未知值同样回落到垂直布局 */
export const getLayoutShape = (layout: LayoutType, isMobile: boolean): LayoutShape => {
  const effective = isMobile ? 'sidebar-nav' : layout;
  const headerFullMenu = effective === 'header-nav';
  const headerRootMenu = effective === 'mixed-nav';
  const menuRail = effective === 'sidebar-mixed-nav';
  const contentOnly = effective === 'full-content';

  return {
    fullWidthHeader: headerFullMenu || headerRootMenu,
    headerFullMenu,
    headerRootMenu,
    menuRail,
    siderSubMenuOnly: headerRootMenu || menuRail,
    sider: !headerFullMenu && !contentOnly,
    contentOnly,
  };
};
