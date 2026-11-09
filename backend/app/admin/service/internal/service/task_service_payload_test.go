// 任务载荷（task_payload，jsonb 列）的空值与非法值处理。
//
// 页面保存时空载荷会以 "" 提交；"" 不是合法 JSON，直接写 jsonb 列会 500。
// 约定：空串（含纯空白）等同没有载荷，存 NULL；非空但不是合法 JSON 返回 400。
package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go-wind-admin/third_party/go-utils/trans"

	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	taskV1 "go-wind-admin/api/gen/go/task/service/v1"
	"go-wind-admin/app/admin/service/internal/data/enttest"
	"go-wind-admin/pkg/middleware/auth"
	paginationV1 "go-wind-admin/third_party/go-crud/api/gen/go/pagination/v1"
)

func TestTaskService_PayloadEmptyAndInvalid(t *testing.T) {
	const typeName = "tasksvc_payload_type"
	svc, _ := newTaskServiceForTest(t, newTaskSchedulerStub(typeName))
	ctx := enttest.NewSystemContext(context.Background())
	opCtx := auth.NewContext(ctx, &authenticationV1.UserTokenPayload{UserId: 7})

	getPayload := func(id uint32) *string {
		got, err := svc.taskRepo.Get(ctx, &taskV1.GetTaskRequest{QueryBy: &taskV1.GetTaskRequest_Id{Id: id}})
		require.NoError(t, err)
		if got.TaskPayload == nil {
			return nil
		}
		return trans.Ptr(got.GetTaskPayload())
	}
	update := func(id uint32, payload string) error {
		_, err := svc.Update(opCtx, &taskV1.UpdateTaskRequest{
			Id:   id,
			Data: &taskV1.Task{Remark: trans.Ptr("r"), TaskPayload: trans.Ptr(payload)},
		})
		return err
	}

	// 新增：空串存 NULL
	_, err := svc.Create(opCtx, &taskV1.CreateTaskRequest{Data: &taskV1.Task{
		Type: taskV1.Task_PERIODIC.Enum(), TypeName: trans.Ptr(typeName),
		CronSpec: trans.Ptr("0 */5 * * * *"), Enable: trans.Ptr(false), TaskPayload: trans.Ptr(""),
	}})
	require.NoError(t, err, "新增任务带空串载荷应成功")
	rows, err := svc.taskRepo.List(ctx, &paginationV1.PagingRequest{NoPaging: trans.Ptr(true)})
	require.NoError(t, err)
	require.Len(t, rows.GetItems(), 1)
	id := rows.GetItems()[0].GetId()
	require.Nil(t, getPayload(id), "空串载荷应存 NULL")

	// 编辑：空载荷什么都不改（页面提交 ""）仍成功，仍是 NULL
	require.NoError(t, update(id, ""))
	require.Nil(t, getPayload(id))
	require.NoError(t, update(id, "  \n"), "纯空白等同空")
	require.Nil(t, getPayload(id))

	// 填 {} 成功
	require.NoError(t, update(id, "{}"))
	require.Equal(t, "{}", *getPayload(id))

	// 清空回 NULL
	require.NoError(t, update(id, ""))
	require.Nil(t, getPayload(id), "已有载荷清空后应回到 NULL")

	// 非 JSON：400，且不改库
	require.NoError(t, update(id, `{"a":1}`))
	err = update(id, "abc")
	require.Error(t, err)
	require.True(t, adminV1.IsBadRequest(err), "非 JSON 载荷应 400，实际 %v", err)
	require.Equal(t, `{"a":1}`, *getPayload(id), "被拒绝的更新不应改动载荷")

	// 新增带非 JSON 同样 400
	_, err = svc.Create(opCtx, &taskV1.CreateTaskRequest{Data: &taskV1.Task{
		Type: taskV1.Task_PERIODIC.Enum(), TypeName: trans.Ptr(typeName),
		CronSpec: trans.Ptr("0 */5 * * * *"), Enable: trans.Ptr(false), TaskPayload: trans.Ptr("abc"),
	}})
	require.Error(t, err)
	require.True(t, adminV1.IsBadRequest(err))
}
