package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"

	entCrud "go-wind-admin/third_party/go-crud/entgo"
	"go-wind-admin/third_party/go-utils/password"

	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/ent/usercredential"
	"go-wind-admin/app/admin/service/internal/data/enttest"
)

// openPostgresForTest 连到 GWA_TEST_POSTGRES_DSN 指向的库并建表；没设这个变量就跳过。
// DSN 必须指向一个可以随便写的空库。SQLite 的写事务本身串行，行锁和唯一索引的并发行为
// 只有在真实 PostgreSQL 上才测得出来。CI 的 backend 工作流里有一个带 postgres 服务的 job 跑这些测试。
func openPostgresForTest(t *testing.T) (*entCrud.EntClient[*ent.Client], context.Context) {
	t.Helper()
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
	return entCrud.NewEntClient[*ent.Client](client, drv), ctx
}

// runConcurrently 同时放行若干个函数，等它们都结束。
func runConcurrently(fns ...func()) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			fn()
		}()
	}
	close(start)
	wg.Wait()
}

// 绑定事务和凭证变更事务对同一个用户串行（真实 PostgreSQL）：两个事务都先锁用户行。
// 一键登录的绑定和短信改号同时发生时，不管谁先拿到锁，最后都没有绑定留下：
// 绑定先提交，随后被改号的事务删掉；改号先提交，绑定事务重新确认时发现号码已变，不写。
func TestBindMiniapp_SerialWithMobileChangeOnPostgres(t *testing.T) {
	entClient, ctx := openPostgresForTest(t)
	client := entClient.Client()
	creds := NewUserCredentialRepoForTest(entClient, password.NewBCryptCrypto(), nil)

	const rounds = 30
	var bindWon, changeWon int
	for round := 0; round < rounds; round++ {
		oldMobile := fmt.Sprintf("135%08d", round*2)
		newMobile := fmt.Sprintf("135%08d", round*2+1)
		uid := client.User.Create().SetUsername(fmt.Sprintf("serial-%d-%d", os.Getpid(), round)).SetMobile(oldMobile).SaveX(ctx).ID
		t.Cleanup(func() {
			client.UserCredential.Delete().Where(usercredential.UserIDEQ(uid)).ExecX(ctx)
			client.User.Delete().Where(user.IDEQ(uid)).ExecX(ctx)
		})
		openid := fmt.Sprintf("openid-serial-%d-%d", os.Getpid(), round)

		var bindErr, changeErr error
		runConcurrently(
			func() { _, bindErr = creds.BindMiniapp(ctx, uid, openid, MiniappBindCheck{Mobile: &oldMobile}) },
			func() { _, changeErr = creds.BindVerifiedPhone(ctx, uid, newMobile) },
		)
		require.NoError(t, changeErr, "第 %d 轮", round)
		switch {
		case bindErr == nil:
			bindWon++
		case errors.Is(bindErr, ErrMiniappBindStale):
			changeWon++
		default:
			t.Fatalf("第 %d 轮：绑定返回了意外的错误 %v", round, bindErr)
		}

		remaining := client.UserCredential.Query().
			Where(usercredential.UserIDEQ(uid), usercredential.ProviderEQ(MiniappProvider)).
			CountX(ctx)
		require.Zero(t, remaining, "第 %d 轮：改号之后不能有绑定留下（bindErr=%v）", round, bindErr)
		require.Equal(t, newMobile, *client.User.GetX(ctx, uid).Mobile, "第 %d 轮", round)
	}
	t.Logf("绑定先拿到锁 %d 轮，改号先拿到锁 %d 轮", bindWon, changeWon)
}

// 并发首次绑定（真实 PostgreSQL）：
//   - 不同用户抢同一个 openid：一个成功，另一个得到 ErrMiniappBoundToOther，库里只有一行；
//   - 同一个用户的两个请求：都成功，返回同一行。
func TestBindMiniapp_ConcurrentOnPostgres(t *testing.T) {
	entClient, ctx := openPostgresForTest(t)
	client := entClient.Client()
	creds := NewUserCredentialRepoForTest(entClient, password.NewBCryptCrypto(), nil)

	bindingCount := func(openid string) int {
		return client.UserCredential.Query().
			Where(usercredential.ProviderEQ(MiniappProvider), usercredential.ProviderAccountIDEQ(openid)).
			CountX(ctx)
	}

	const rounds = 30
	for round := 0; round < rounds; round++ {
		mobileA := fmt.Sprintf("137%08d", round*2)
		mobileB := fmt.Sprintf("137%08d", round*2+1)
		a := client.User.Create().SetUsername(fmt.Sprintf("bind-a-%d-%d", os.Getpid(), round)).SetMobile(mobileA).SaveX(ctx).ID
		b := client.User.Create().SetUsername(fmt.Sprintf("bind-b-%d-%d", os.Getpid(), round)).SetMobile(mobileB).SaveX(ctx).ID
		t.Cleanup(func() {
			client.UserCredential.Delete().Where(usercredential.UserIDIn(a, b)).ExecX(ctx)
			client.User.Delete().Where(user.IDIn(a, b)).ExecX(ctx)
		})

		// 不同用户抢同一个 openid。
		contended := fmt.Sprintf("openid-contended-%d-%d", os.Getpid(), round)
		var idA, idB uint32
		var errA, errB error
		runConcurrently(
			func() { idA, errA = creds.BindMiniapp(ctx, a, contended, MiniappBindCheck{Mobile: &mobileA}) },
			func() { idB, errB = creds.BindMiniapp(ctx, b, contended, MiniappBindCheck{Mobile: &mobileB}) },
		)
		require.Equal(t, 1, bindingCount(contended), "第 %d 轮：一个 openid 只能有一行绑定（errA=%v errB=%v）", round, errA, errB)
		switch {
		case errA == nil:
			require.NotZero(t, idA)
			require.True(t, errors.Is(errB, ErrMiniappBoundToOther), "第 %d 轮：输的一方应得到冲突，got %v", round, errB)
		case errB == nil:
			require.NotZero(t, idB)
			require.True(t, errors.Is(errA, ErrMiniappBoundToOther), "第 %d 轮：输的一方应得到冲突，got %v", round, errA)
		default:
			t.Fatalf("第 %d 轮：两方都失败 errA=%v errB=%v", round, errA, errB)
		}

		// 同一个用户的两个请求。
		same := fmt.Sprintf("openid-same-%d-%d", os.Getpid(), round)
		var id1, id2 uint32
		var err1, err2 error
		runConcurrently(
			func() { id1, err1 = creds.BindMiniapp(ctx, a, same, MiniappBindCheck{Mobile: &mobileA}) },
			func() { id2, err2 = creds.BindMiniapp(ctx, a, same, MiniappBindCheck{Mobile: &mobileA}) },
		)
		require.NoError(t, err1, "第 %d 轮", round)
		require.NoError(t, err2, "第 %d 轮", round)
		require.Equal(t, id1, id2, "第 %d 轮：两个请求拿到同一行", round)
		require.Equal(t, 1, bindingCount(same), "第 %d 轮", round)
	}
}
