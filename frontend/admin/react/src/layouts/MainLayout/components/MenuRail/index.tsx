import { useNavigate } from 'react-router-dom';

import { usePreferencesStore } from '@/core/preferences/store';
import { useMenuItems } from '../../hooks/useMenuItems';
import { findFirstLeafPath, type LayoutMenuNode } from '../../utils/layoutMenu';
import './MenuRail.style.less';

interface MenuRailProps {
  menuData: LayoutMenuNode[];
  activePath?: string;
}

/** 双列菜单布局的第一列：一级菜单的图标 + 标题 */
export const MenuRail = ({ menuData, activePath }: MenuRailProps) => {
  const navigate = useNavigate();
  const logo = usePreferencesStore((state) => state.preferences.logo);
  const items = useMenuItems(menuData);

  const handleClick = (path: string) => {
    const root = menuData.find((item) => item.path === path);
    const target = root ? findFirstLeafPath(root) : path;
    if (target) navigate(target);
  };

  return (
    <nav className="app-menu-rail">
      {logo.enable && (
        <button type="button" className="app-menu-rail-logo" onClick={() => navigate('/')}>
          <img src={logo.source} alt="logo" />
        </button>
      )}
      <div className="app-menu-rail-list">
        {items.map((item) => (
          <button
            key={item.key}
            type="button"
            title={item.label}
            className={`app-menu-rail-item${item.key === activePath ? ' active' : ''}`}
            aria-current={item.key === activePath ? 'page' : undefined}
            onClick={() => handleClick(item.key)}
          >
            <span className="app-menu-rail-icon">{item.icon}</span>
            <span className="app-menu-rail-label">{item.label}</span>
          </button>
        ))}
      </div>
    </nav>
  );
};

export default MenuRail;
