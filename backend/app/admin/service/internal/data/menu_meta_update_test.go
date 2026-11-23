package data

import (
	"context"
	stdsql "database/sql"
	"errors"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"go-wind-admin/third_party/go-utils/trans"

	permissionV1 "go-wind-admin/api/gen/go/permission/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/enttest"
	entCrud "go-wind-admin/third_party/go-crud/entgo"
)

// meta 的更新 SQL 是 PostgreSQL 专用（jsonb_build_object、jsonb - text[]），SQLite 跑不了，
// 所以这里用一个只记录语句、不连库的驱动看 Update 实际发出的 UPDATE。

type recordingDriver struct {
	queries []string
	args    [][]any
}

func (d *recordingDriver) Exec(_ context.Context, query string, args, v any) error {
	d.queries = append(d.queries, query)
	a, _ := args.([]any)
	d.args = append(d.args, a)
	if r, ok := v.(*stdsql.Result); ok {
		*r = stdResult(1)
	}
	return nil
}

func (d *recordingDriver) Query(context.Context, string, any, any) error {
	return errors.New("recordingDriver: query not supported")
}

func (d *recordingDriver) Tx(context.Context) (dialect.Tx, error) { return dialect.NopTx(d), nil }
func (d *recordingDriver) Close() error                           { return nil }
func (d *recordingDriver) Dialect() string                        { return dialect.Postgres }

type stdResult int64

func (r stdResult) LastInsertId() (int64, error) { return 0, nil }
func (r stdResult) RowsAffected() (int64, error) { return int64(r), nil }

func newMenuRepoRecording(t *testing.T) (*MenuRepo, *recordingDriver) {
	t.Helper()
	drv := &recordingDriver{}
	client := ent.NewClient(ent.Driver(drv))
	return NewMenuRepoForTest(entCrud.NewEntClient[*ent.Client](client, nil)), drv
}

func updateMenuSQL(t *testing.T, data *permissionV1.Menu, paths ...string) (string, []any, error) {
	t.Helper()
	return runMenuUpdate(t, &permissionV1.UpdateMenuRequest{
		Id:         50,
		Data:       data,
		UpdateMask: &fieldmaskpb.FieldMask{Paths: paths},
	})
}

// updateMenuJSON 按 HTTP 层的方式用 protojson 解析请求体：FieldMask 的路径会被转成蛇形。
func updateMenuJSON(t *testing.T, body string) (string, []any, error) {
	t.Helper()
	req := &permissionV1.UpdateMenuRequest{}
	require.NoError(t, protojson.Unmarshal([]byte(body), req))
	return runMenuUpdate(t, req)
}

func runMenuUpdate(t *testing.T, req *permissionV1.UpdateMenuRequest) (string, []any, error) {
	t.Helper()
	repo, drv := newMenuRepoRecording(t)
	ctx := enttest.NewSystemContext(context.Background())
	err := repo.Update(ctx, req)
	if err != nil {
		return "", nil, err
	}
	require.Len(t, drv.queries, 1)
	return drv.queries[0], drv.args[0], nil
}

// 管理页抽屉的保存（请求体与前端发出的一致）：四个隐藏开关、activePath 和权限标识列表都要写进 meta，
// 没列出的 meta 键不动。修复前 meta.activePath 被转成 meta_active_path，整个请求 invalid field mask；
// 列表字段只写了键没写值，jsonb_build_object 参数成奇数。
func TestMenuUpdate_MetaSubPathsWritten(t *testing.T) {
	q, args, err := updateMenuJSON(t, `{"id":50,"data":{"path":"/log","meta":{"hideInMenu":true,"hideChildrenInMenu":true,"hideInBreadcrumb":true,"hideInTab":false,"activePath":"/log/login","authority":["sys:platform_admin","x'y"]}},`+
		`"updateMask":"path,meta.hideInMenu,meta.hideChildrenInMenu,meta.hideInBreadcrumb,meta.hideInTab,meta.activePath,meta.authority,id"}`)
	require.NoError(t, err)

	require.Contains(t, q, `"path" = $`)
	require.Contains(t, q, `"meta" = COALESCE("meta", '{}'::jsonb) || jsonb_build_object('hideInMenu',true,'hideChildrenInMenu',true,'hideInBreadcrumb',true,'hideInTab',false,'activePath','/log/login','authority',jsonb_build_array('sys:platform_admin','x''y'))`)
	require.Equal(t, 1, strings.Count(q, `"meta" =`), q)
	require.Contains(t, args, "/log")
}

// 掩码里列了但请求没给值的 meta 字段要删掉这个键（清空 activePath），
// 和同一请求里要写的键合成一条 SET；分两条 SET 时 PostgreSQL 报 multiple assignments。
func TestMenuUpdate_MetaClearAndSetInOneAssignment(t *testing.T) {
	q, _, err := updateMenuSQL(t, &permissionV1.Menu{
		Meta: &permissionV1.MenuMeta{HideInMenu: trans.Ptr(false)},
	}, "meta.hideInMenu", "meta.activePath", "id")
	require.NoError(t, err)

	require.Contains(t, q, `"meta" = (COALESCE("meta", '{}'::jsonb) - '{activePath}'::text[]) || jsonb_build_object('hideInMenu',false)`)
	require.Equal(t, 1, strings.Count(q, `"meta" =`), q)
}

// 掩码不提 meta 时不碰 meta 列。
func TestMenuUpdate_NoMetaPathLeavesMetaAlone(t *testing.T) {
	q, _, err := updateMenuSQL(t, &permissionV1.Menu{
		Path: trans.Ptr("/log"),
		Meta: &permissionV1.MenuMeta{HideInMenu: trans.Ptr(true)},
	}, "path", "id")
	require.NoError(t, err)
	require.NotContains(t, q, `"meta"`)
}

// 整段 meta 以前 200 但什么都不写；现在要求逐字段列出。未知字段同样报错。
func TestMenuUpdate_RejectsWholeMetaAndUnknownMetaPath(t *testing.T) {
	data := &permissionV1.Menu{Meta: &permissionV1.MenuMeta{HideInMenu: trans.Ptr(true)}}

	_, _, err := updateMenuSQL(t, data, "meta", "id")
	require.Error(t, err)
	require.True(t, permissionV1.IsBadRequest(err), err)

	_, _, err = updateMenuSQL(t, data, "meta.no_such_field", "id")
	require.Error(t, err)
	require.True(t, permissionV1.IsBadRequest(err), err)
}
