package server

import (
	"context"
	"testing"

	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
)

// 没有单独授权字典和收件箱接口的角色，登录后要能看到字典中文和自己的铃铛；
// 字典的增删改、别人的消息管理仍要过接口鉴权。
func TestAuthzRequiredMatcher(t *testing.T) {
	required := newAuthzRequiredMatcher()
	for op, want := range map[string]bool{
		adminV1.OperationDictEntryServiceList:                         false,
		adminV1.OperationDictTypeServiceList:                          false,
		adminV1.OperationInternalMessageRecipientServiceListUserInbox: false,
		adminV1.OperationDictEntryServiceCreate:                       true,
		adminV1.OperationDictEntryServiceDelete:                       true,
		adminV1.OperationDictTypeServiceUpdate:                        true,
		adminV1.OperationUserServiceList:                              true,
	} {
		if got := required(context.Background(), op); got != want {
			t.Errorf("%s: authz required = %v, want %v", op, got, want)
		}
	}
}
