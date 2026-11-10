import type {
  permissionservicev1_Menu as Menu,
  permissionservicev1_MenuMeta as MenuMeta,
} from '@/api/generated/admin/service/v1';
import { normalizeAuthority } from './constants';

// 菜单抽屉的表单项名是扁平的 'meta.<字段>'，和接口里嵌套的 meta 互转。

const BOOLEAN_META_FIELDS = [
  'keepAlive',
  'affixTab',
  'hideInMenu',
  'hideChildrenInMenu',
  'hideInBreadcrumb',
  'hideInTab',
] as const;

const TOP_LEVEL_FIELDS = ['type', 'status', 'path', 'component', 'parentId'] as const;

/** 编辑时回显：接口返回的菜单 → 表单值 */
export function menuToFormValues(menu: Menu): Record<string, unknown> {
  const meta: Partial<MenuMeta> = menu.meta ?? {};
  const values: Record<string, unknown> = {
    type: menu.type || 'MENU',
    status: menu.status || 'ON',
    path: menu.path || '',
    component: menu.component || '',
    parentId: menu.parentId || undefined,
    'meta.title': meta.title || '',
    'meta.order': meta.order || 0,
    'meta.icon': meta.icon || '',
    'meta.authority': normalizeAuthority(meta.authority),
    'meta.activePath': meta.activePath || '',
  };
  for (const field of BOOLEAN_META_FIELDS) {
    values[`meta.${field}`] = meta[field] || false;
  }
  return values;
}

/**
 * 提交：表单值 → 菜单请求体。
 * 只带 values 里出现的字段：DrawerForm 的 onFinish 只给当前渲染的表单项，按类型隐藏的项
 * （目录没有组件路径、权限标识）不在里面。编辑时这些字段不能进 updateMask，否则会被清空。
 */
export function formValuesToMenu(values: Record<string, unknown>): Record<string, unknown> {
  const payload: Record<string, unknown> = {};
  for (const key of TOP_LEVEL_FIELDS) {
    if (key in values) payload[key] = values[key];
  }
  if ('parentId' in payload) payload.parentId = payload.parentId || 0;

  const meta: Record<string, unknown> = {};
  const has = (field: string) => `meta.${field}` in values;
  const get = (field: string) => values[`meta.${field}`];
  if (has('title')) meta.title = get('title');
  if (has('order')) meta.order = get('order') || 0;
  if (has('icon')) meta.icon = get('icon') || '';
  if (has('authority')) meta.authority = get('authority') || [];
  // 清空时不给值：后端按掩码删掉这个键
  if (has('activePath')) meta.activePath = String(get('activePath') ?? '').trim() || undefined;
  for (const field of BOOLEAN_META_FIELDS) {
    if (has(field)) meta[field] = Boolean(get(field));
  }
  payload.meta = meta;
  return payload;
}
