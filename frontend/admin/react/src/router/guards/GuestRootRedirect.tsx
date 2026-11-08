import { Navigate } from 'react-router-dom';

import { ROUTES } from '@/config/constants';
import { useAuthStore } from '@/stores';

/**
 * 未登录时构建的路由表里 '/' 的落点。
 *
 * 这张路由表没有菜单，不能套用「无菜单进个人中心」的兜底，否则：
 * - 未登录打开 '/' 会变成 login?redirect=/opm/profile，登录后落到个人中心；
 * - 登录瞬间这张表还在用，'/' 也会被解析成个人中心，新路由表随后沿用这个地址。
 *
 * 已有 token 说明刚登录成功，路由表马上重建，这里什么都不做，让地址停在 '/'，
 * 由新路由表解析到首个可用菜单。
 */
export const GuestRootRedirect = () => {
  const accessToken = useAuthStore((s) => s.accessToken);
  return accessToken ? null : <Navigate to={ROUTES.LOGIN} replace />;
};
