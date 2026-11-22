package data

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"

	entCrud "go-wind-admin/third_party/go-crud/entgo"
	"go-wind-admin/third_party/go-utils/password"
	"go-wind-admin/third_party/go-utils/trans"
	"go-wind-admin/third_party/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/third_party/kratos-bootstrap/logger"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"

	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/enttest"
)

// 不同用户并发改到同一个手机号：最多一方提交，另一方得到 CONFLICT。
//
// SQLite 的写事务本身串行，测不出问题，这条只在真实 PostgreSQL 上有意义。
// 设置 GWA_TEST_POSTGRES_DSN 才跑；DSN 必须指向一个可以随便写的空库（测试会在里面建表、写用户行）。
// 例：GWA_TEST_POSTGRES_DSN="host=localhost port=5432 user=postgres password=... dbname=gwa_scratch sslmode=disable"
func TestClaimMobile_ConcurrentUsersOnPostgres(t *testing.T) {
	dsn := os.Getenv("GWA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GWA_TEST_POSTGRES_DSN not set")
	}

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	drv := entsql.OpenDB(dialect.Postgres, db)
	client := ent.NewClient(ent.Driver(drv))
	ctx := enttest.NewSystemContext(context.Background())
	require.NoError(t, client.Schema.Create(ctx))
	entClient := entCrud.NewEntClient[*ent.Client](client, drv)

	boot := bootstrap.NewContextWithParam(context.Background(), nil, nil, bLogger.NopLogger())
	users := NewUserRepo(boot, entClient,
		NewUserRoleRepo(boot, entClient), NewUserOrgUnitRepo(boot, entClient), NewUserPositionRepo(boot, entClient), nil)
	creds := NewUserCredentialRepoForTest(entClient, password.NewBCryptCrypto(), nil)

	const rounds = 30
	for round := 0; round < rounds; round++ {
		mobile := fmt.Sprintf("139%08d", round)
		a := client.User.Create().SetUsername(fmt.Sprintf("claim-a-%d-%d", os.Getpid(), round)).SaveX(ctx).ID
		b := client.User.Create().SetUsername(fmt.Sprintf("claim-b-%d-%d", os.Getpid(), round)).SaveX(ctx).ID
		t.Cleanup(func() { client.User.Delete().Where(user.IDIn(a, b)).ExecX(ctx) })

		// 一方走自助改号，一方走管理员改号：两个入口遵守同一条规则。
		errs := make([]error, 2)
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, errs[0] = creds.BindVerifiedPhone(ctx, a, mobile)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, errs[1] = users.UpdateGuarded(ctx, &identityV1.UpdateUserRequest{
				Id: b, Data: &identityV1.User{Mobile: trans.Ptr(mobile)}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"mobile"}},
			}, nil)
		}()
		close(start)
		wg.Wait()

		owners := client.User.Query().Where(user.MobileEQ(mobile)).CountX(ctx)
		require.Equal(t, 1, owners, "第 %d 轮：号码只能落在一个用户上（errs=%v）", round, errs)
		var succeeded int
		for _, e := range errs {
			if e == nil {
				succeeded++
				continue
			}
			require.True(t, authenticationV1.IsConflict(e) || identityV1.IsConflict(e), "第 %d 轮：输的一方应得到 CONFLICT，got %v", round, e)
		}
		require.Equal(t, 1, succeeded, "第 %d 轮", round)
	}
}
