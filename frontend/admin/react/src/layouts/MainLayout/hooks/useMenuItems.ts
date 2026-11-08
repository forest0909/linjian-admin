import { useMemo, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { getIconFromName } from '../utils/iconResolver';
import type { LayoutMenuNode } from '../utils/layoutMenu';

// 用 type 而不是 interface：antd 的 MenuItemType 带 data-* 索引签名，interface 赋值不过去
type LayoutMenuLeaf = {
  key: string;
  icon: ReactNode;
  label: string;
};

/** antd 区分菜单项和子菜单：子菜单的 children 必填，普通项没有 children */
export type LayoutMenuItem = LayoutMenuLeaf | (LayoutMenuLeaf & { children: LayoutMenuItem[] });

/** 把 useMenuData 的菜单树转成 antd Menu 的 items，并翻译标题。侧栏、顶栏、图标栏共用。 */
export const useMenuItems = (menuData: LayoutMenuNode[]) => {
  const { i18n, t } = useTranslation();
  const { t: tRoutes } = useTranslation('routes');

  return useMemo(() => {
    /**
     * 翻译菜单标题
     * @param label - 可能是 i18n key 或普通文本
     * @returns 翻译后的文本
     */
    const translateLabel = (label: string | undefined): string => {
      if (!label) return '';
      // 处理 'menu:xxx' 或 'routes:xxx' 前缀的 i18n key
      if (label.startsWith('menu:') || label.startsWith('routes:')) {
        const keyName = label.includes(':') ? label.substring(label.indexOf(':') + 1) : label;
        return tRoutes(keyName, { defaultValue: label });
      }
      // 否则直接尝试翻译（可能已经是简化的 key）
      const commonHit = t(label, label);
      if (commonHit !== label) return commonHit;
      // 后端菜单 meta.title 无前缀、用 vue 端种子键位（menu.* / page.*），
      // 按 routes 命名空间的嵌套种子键兜底（routes.json 的 menu/page 段）
      return tRoutes(label, { defaultValue: label });
    };

    const transformItem = (items: LayoutMenuNode[]): LayoutMenuItem[] => {
      return items.map((item) => {
        const leaf: LayoutMenuLeaf = {
          key: item.path ?? '',
          icon: getIconFromName(item.icon),
          label: translateLabel(item.name), // 翻译标题
        };
        return item.children ? { ...leaf, children: transformItem(item.children) } : leaf;
      });
    };
    return transformItem(menuData);
  }, [menuData, t, tRoutes, i18n.language]); // i18n.language：语言切换时重新翻译
};
