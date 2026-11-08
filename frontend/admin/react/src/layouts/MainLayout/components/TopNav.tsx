import { useMemo } from 'react';
import { Menu } from 'antd';
import { useNavigate } from 'react-router-dom';

import { usePreferencesStore } from '@/core/preferences/store';
import { useMenuItems } from '../hooks/useMenuItems';
import { findFirstLeafPath, type LayoutMenuNode } from '../utils/layoutMenu';

interface TopNavProps {
  menuData: LayoutMenuNode[];
  isDark: boolean;
  selectedKeys: string[];
  /** true 时只显示一级菜单，点击跳到该一级的第一个叶子（混合菜单） */
  rootOnly: boolean;
}

/** 水平、混合菜单布局下放进顶栏左侧的品牌区 + 水平菜单 */
export const TopNav = ({ menuData, isDark, selectedKeys, rootOnly }: TopNavProps) => {
  const navigate = useNavigate();
  const logo = usePreferencesStore((state) => state.preferences.logo);
  const app = usePreferencesStore((state) => state.preferences.app);

  const fullItems = useMenuItems(menuData);
  const items = useMemo(
    () =>
      rootOnly
        ? fullItems.map(({ key, icon, label }) => ({ key, icon, label }))
        : fullItems,
    [fullItems, rootOnly],
  );

  const handleClick = ({ key }: { key: string }) => {
    const root = rootOnly ? menuData.find((item) => item.path === key) : undefined;
    const target = root ? findFirstLeafPath(root) : key;
    if (target) navigate(target);
  };

  return (
    <>
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          paddingRight: 16,
          cursor: 'pointer',
          flexShrink: 0,
        }}
        onClick={() => navigate('/')}
      >
        {logo.enable && (
          <img src={logo.source} alt="logo" style={{ height: 32, width: 32, flexShrink: 0 }} />
        )}
        {app.dynamicTitle && (
          <span
            style={{
              fontWeight: 700,
              fontSize: 16,
              color: 'var(--ant-color-text)',
              whiteSpace: 'nowrap',
            }}
          >
            {app.name}
          </span>
        )}
      </div>
      <Menu
        mode="horizontal"
        items={items}
        selectedKeys={selectedKeys}
        onClick={handleClick}
        theme={isDark ? 'dark' : 'light'}
        style={{
          flex: 1,
          minWidth: 0,
          lineHeight: '48px',
          background: 'transparent',
          borderBottom: 'none',
        }}
      />
    </>
  );
};

export default TopNav;
