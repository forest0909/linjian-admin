import {
  useMutation,
  type UseMutationOptions,
  useQuery,
  type UseQueryOptions,
} from '@tanstack/react-query';
import type { authenticationservicev1_ListMiniappBindingsResponse } from '@/api/generated/admin/service/v1';
import { apiClient } from '@/api/client';

/** 查询用户的微信小程序绑定 */
export function useListMiniappBindings(
  userId: number,
  options?: Partial<UseQueryOptions<authenticationservicev1_ListMiniappBindingsResponse, Error>>,
) {
  return useQuery({
    queryKey: ['listMiniappBindings', userId],
    queryFn: () => apiClient.miniappAuthService.ListUserBindings({ userId }),
    ...options,
  });
}

/** 解除用户的一条微信小程序绑定（幂等） */
export function useDeleteMiniappBinding(
  options?: UseMutationOptions<{}, Error, { userId: number; id: number }>,
) {
  return useMutation({
    mutationFn: ({ userId, id }: { userId: number; id: number }) =>
      apiClient.miniappAuthService.DeleteUserBinding({ userId, id }),
    ...options,
  });
}
