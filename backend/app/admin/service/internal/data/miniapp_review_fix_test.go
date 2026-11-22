package data

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go-wind-admin/third_party/go-utils/password"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"

	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/ent/usercredential"
	"go-wind-admin/app/admin/service/internal/data/ent/userrole"
	"go-wind-admin/app/admin/service/internal/data/enttest"
)

// A345 审查修复的数据层用例。连接池只留一个连接的用例是确定性的：
// 事务占着唯一的连接时，任何「事务外再要一个连接」的写法都会等到超时。

// 绑定撞 openid 唯一索引之后的归属重读发生在失败事务回滚之后（审查第 5 条）。
// 连接池只有一个连接：重读如果还在事务里做，就要等自己占着的连接，直到 ctx 超时。
func TestBindMiniapp_ConflictRereadAfterRollback_SingleConn(t *testing.T) {
	entClient := enttest.NewEntClientForTest(t)
	client := entClient.Client()
	sys := enttest.NewSystemContext(context.Background())
	creds := NewUserCredentialRepoForTest(entClient, password.NewBCryptCrypto(), nil)

	mobile := "13800000001"
	a := client.User.Create().SetUsername("conflict-a").SetMobile(mobile).SaveX(sys).ID
	b := client.User.Create().SetUsername("conflict-b").SetMobile("13800000002").SaveX(sys).ID
	const openid = "openid-conflict-0001"

	// 在 a 的绑定事务里、插入之前，用同一个事务先给 b 插入同一个 openid：
	// 随后 a 的插入真的撞上唯一索引。事务回滚后两行都不在，重读读不到已提交的行。
	var injected atomic.Bool
	client.UserCredential.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			cm, ok := m.(*ent.UserCredentialMutation)
			if ok && m.Op().Is(ent.OpCreate) && injected.CompareAndSwap(false, true) {
				cm.Client().UserCredential.Create().
					SetUserID(b).SetTenantID(0).
					SetIdentityType(usercredential.IdentityTypeSocialOauth).SetIdentifier(openid).
					SetCredentialType(usercredential.CredentialTypeOauthToken).
					SetIsPrimary(false).SetStatus(usercredential.StatusEnabled).
					SetProvider(MiniappProvider).SetProviderAccountID(openid).
					SetCreatedAt(time.Now()).
					ExecX(ctx)
			}
			return next.Mutate(ctx, m)
		})
	})

	entClient.Driver().DB().SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := creds.BindMiniapp(ctx, a, openid, MiniappBindCheck{Mobile: &mobile})
	require.True(t, injected.Load(), "钩子应该执行过")
	require.ErrorIs(t, err, ErrMiniappBindUnconfirmed, "撞索引后重读不到已提交的行：归属无法确认（不是等连接等到超时）")
	require.NoError(t, ctx.Err(), "没有等到超时")

	// 连接和事务都放掉了：同一个池还能用，库里没有留下任何绑定。
	require.Zero(t, client.UserCredential.Query().Where(usercredential.ProviderEQ(MiniappProvider)).CountX(sys))
}

// 撞的不是 openid 唯一索引（别的约束错误）不走「重读归属」，原样报错。
func TestIsMiniappBindingConflict(t *testing.T) {
	entClient := enttest.NewEntClientForTest(t)
	client := entClient.Client()
	sys := enttest.NewSystemContext(context.Background())

	create := func(uid uint32, openid string) error {
		return client.UserCredential.Create().
			SetUserID(uid).SetTenantID(0).
			SetIdentityType(usercredential.IdentityTypeSocialOauth).SetIdentifier(openid).
			SetCredentialType(usercredential.CredentialTypeOauthToken).
			SetIsPrimary(false).SetStatus(usercredential.StatusEnabled).
			SetProvider(MiniappProvider).SetProviderAccountID(openid).
			Exec(sys)
	}
	require.NoError(t, create(1, "openid-dup"))
	err := create(2, "openid-dup")
	require.True(t, ent.IsConstraintError(err), "got %v", err)
	require.True(t, isMiniappBindingConflict(err))

	require.False(t, isMiniappBindingConflict(nil))
	require.False(t, isMiniappBindingConflict(fmt.Errorf("some other failure")))
	// 别的唯一约束（用户名）不是归属冲突。
	client.User.Create().SetUsername("dup-name").SaveX(sys)
	_, err = client.User.Create().SetUsername("dup-name").Save(sys)
	require.True(t, ent.IsConstraintError(err), "got %v", err)
	require.False(t, isMiniappBindingConflict(err))
}

// 自助改密的口令策略在开事务之前读好（审查第 6 条）。
// 配置缓存是冷的、连接池只有一个连接：事务里再用普通连接查 sys_config 的话，会等自己占着的连接直到超时。
func TestChangeCredential_ColdPolicyCache_SingleConn(t *testing.T) {
	ur, cr := newUserAndCredentialRepoSqlite(t)
	sys := enttest.NewSystemContext(context.Background())
	uid := createUserWithPassword(t, ur, cr, sys, 0, "policy-user", "OldPass@1234")

	// 清空配置缓存，保证读策略时一定要查库。
	coolDown := func() {
		cr.configRepo.cacheMu.Lock()
		cr.configRepo.cache = make(map[string]sysConfigCacheEntry)
		cr.configRepo.cacheMu.Unlock()
	}
	coolDown()

	cr.entClient.Driver().DB().SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(sys, 5*time.Second)
	defer cancel()

	require.NoError(t, cr.ChangeCredential(ctx, uid, &authenticationV1.ChangeCredentialRequest{
		IdentityType:  authenticationV1.UserCredential_USERNAME,
		Identifier:    "policy-user",
		OldCredential: "OldPass@1234",
		NewCredential: "NewPass@5678",
	}))
	require.NoError(t, ctx.Err(), "没有等到超时")

	// 管理员重置密码走另一个事务入口，同样先读策略。
	coolDown()
	require.NoError(t, cr.ResetCredentialGuarded(ctx, uid, &authenticationV1.ResetCredentialRequest{
		IdentityType:  authenticationV1.UserCredential_USERNAME,
		Identifier:    "policy-user",
		NewCredential: "Reset@Pass9012",
	}, func(context.Context, *ent.Tx, []uint32) error { return nil }))
	require.NoError(t, ctx.Err(), "没有等到超时")
}

// 签发前在用户行锁下重新确认会话的依据（审查第 3 条）：绑定不在了、密码变了、用户停用了都不执行签发。
func TestIssueUnderUserLock_Reconfirms(t *testing.T) {
	entClient := enttest.NewEntClientForTest(t)
	client := entClient.Client()
	sys := enttest.NewSystemContext(context.Background())
	creds := NewUserCredentialRepoForTest(entClient, password.NewBCryptCrypto(), nil)

	mobile := "13800000001"
	uid := client.User.Create().SetUsername("issue-user").SetMobile(mobile).SaveX(sys).ID
	client.UserCredential.Create().
		SetUserID(uid).SetTenantID(0).
		SetIdentityType(usercredential.IdentityTypeUsername).SetIdentifier("issue-user").
		SetCredentialType(usercredential.CredentialTypePasswordHash).SetCredential("hash-1").
		SetIsPrimary(true).SetStatus(usercredential.StatusEnabled).
		ExecX(sys)
	bindingID, err := creds.BindMiniapp(sys, uid, "openid-issue-0001", MiniappBindCheck{Mobile: &mobile})
	require.NoError(t, err)

	issued := 0
	issue := func() error { issued++; return nil }
	ctx := context.Background()
	current, stale := "hash-1", "hash-0"

	// 依据都在：执行签发。
	require.NoError(t, creds.IssueUnderUserLock(ctx, uid, MiniappIssueGuard{BindingID: bindingID, PasswordSnapshot: &current}, issue))
	require.NoError(t, creds.IssueUnderUserLock(ctx, uid, MiniappIssueGuard{}, issue), "H5 会话没有绑定也没有密码快照")
	require.Equal(t, 2, issued)

	// 密码变了。
	require.ErrorIs(t, creds.IssueUnderUserLock(ctx, uid, MiniappIssueGuard{PasswordSnapshot: &stale}, issue), ErrMiniappBindStale)
	// 绑定是别的用户的 ID、被停用、被删掉。
	require.True(t, ent.IsNotFound(creds.IssueUnderUserLock(ctx, uid+1000, MiniappIssueGuard{BindingID: bindingID}, issue)), "用户不存在")
	client.UserCredential.UpdateOneID(bindingID).SetStatus(usercredential.StatusDisabled).ExecX(sys)
	require.ErrorIs(t, creds.IssueUnderUserLock(ctx, uid, MiniappIssueGuard{BindingID: bindingID}, issue), ErrMiniappBindingGone)
	client.UserCredential.UpdateOneID(bindingID).SetStatus(usercredential.StatusEnabled).ExecX(sys)
	require.NoError(t, creds.DeleteMiniappBinding(sys, uid, bindingID, MiniappUnbindGuard{}))
	require.ErrorIs(t, creds.IssueUnderUserLock(ctx, uid, MiniappIssueGuard{BindingID: bindingID}, issue), ErrMiniappBindingGone)
	// 用户停用。
	client.User.UpdateOneID(uid).SetStatus(user.StatusLocked).ExecX(sys)
	require.ErrorIs(t, creds.IssueUnderUserLock(ctx, uid, MiniappIssueGuard{}, issue), ErrMiniappUserDisabled)

	require.Equal(t, 2, issued, "确认不通过时没有执行签发")
}

// 解绑的复核在删除事务里做（审查第 4 条）：Check 拿到的是事务里读到的目标用户当前角色，
// 返回错误时什么都不删；删除条件限定用户和绑定 ID。
func TestDeleteMiniappBinding_GuardRunsInTx(t *testing.T) {
	entClient := enttest.NewEntClientForTest(t)
	client := entClient.Client()
	sys := enttest.NewSystemContext(context.Background())
	creds := NewUserCredentialRepoForTest(entClient, password.NewBCryptCrypto(), nil)

	mobile := "13800000001"
	uid := client.User.Create().SetUsername("guard-user").SetMobile(mobile).SaveX(sys).ID
	bindingID, err := creds.BindMiniapp(sys, uid, "openid-guard-0001", MiniappBindCheck{Mobile: &mobile})
	require.NoError(t, err)
	client.UserRole.Create().SetUserID(uid).SetRoleID(31).ExecX(sys)
	client.UserRole.Create().SetUserID(uid).SetRoleID(32).ExecX(sys)

	rejected := fmt.Errorf("rejected by check")
	var seen []uint32
	err = creds.DeleteMiniappBinding(sys, uid, bindingID, MiniappUnbindGuard{Check: func(_ context.Context, roleIDs []uint32) error {
		seen = roleIDs
		return rejected
	}})
	require.ErrorIs(t, err, rejected)
	require.ElementsMatch(t, []uint32{31, 32}, seen)
	require.Equal(t, 1, client.UserCredential.Query().Where(usercredential.IDEQ(bindingID)).CountX(sys), "检查不通过：绑定还在")

	// 目标用户不存在。
	err = creds.DeleteMiniappBinding(sys, uid+1000, bindingID, MiniappUnbindGuard{})
	require.ErrorIs(t, err, ErrMiniappTargetNotFound)
	require.Equal(t, 1, client.UserCredential.Query().Where(usercredential.IDEQ(bindingID)).CountX(sys))

	// 检查通过：删掉；再删一次仍然成功（幂等）。
	pass := MiniappUnbindGuard{Check: func(context.Context, []uint32) error { return nil }}
	require.NoError(t, creds.DeleteMiniappBinding(sys, uid, bindingID, pass))
	require.NoError(t, creds.DeleteMiniappBinding(sys, uid, bindingID, pass))
	require.Zero(t, client.UserCredential.Query().Where(usercredential.IDEQ(bindingID)).CountX(sys))
}

// ---- 真实 PostgreSQL：行锁的并发行为 ----

// 解绑的权限复核和并发的角色提升对同一个用户串行（审查第 4 条）。
// 角色提升先拿到用户行锁、还没提交时解绑进来：解绑等锁，拿到锁后读到的是提升之后的角色。
func TestDeleteMiniappBinding_SeesConcurrentRolePromotionOnPostgres(t *testing.T) {
	entClient, ctx := openPostgresForTest(t)
	client := entClient.Client()
	creds := NewUserCredentialRepoForTest(entClient, password.NewBCryptCrypto(), nil)

	mobile := fmt.Sprintf("136%08d", os.Getpid()%100000000)
	uid := client.User.Create().SetUsername(fmt.Sprintf("promote-%d", os.Getpid())).SetMobile(mobile).SaveX(ctx).ID
	t.Cleanup(func() {
		client.UserRole.Delete().Where(userrole.UserIDEQ(uid)).ExecX(ctx)
		client.UserCredential.Delete().Where(usercredential.UserIDEQ(uid)).ExecX(ctx)
		client.User.Delete().Where(user.IDEQ(uid)).ExecX(ctx)
	})
	bindingID, err := creds.BindMiniapp(ctx, uid, fmt.Sprintf("openid-promote-%d", os.Getpid()), MiniappBindCheck{Mobile: &mobile})
	require.NoError(t, err)
	const promotedRole = 990001

	// 角色提升：锁用户行，加角色，停一会儿再提交。
	locked := make(chan struct{})
	promoted := make(chan error, 1)
	go func() {
		promoted <- func() error {
			tx, terr := client.Tx(ctx)
			if terr != nil {
				close(locked)
				return terr
			}
			if _, terr = lockUserRow(ctx, tx, entClient.Driver().Dialect(), uid); terr != nil {
				close(locked)
				_ = tx.Rollback()
				return terr
			}
			close(locked)
			if terr = tx.UserRole.Create().SetUserID(uid).SetRoleID(promotedRole).Exec(ctx); terr != nil {
				_ = tx.Rollback()
				return terr
			}
			time.Sleep(300 * time.Millisecond)
			return tx.Commit()
		}()
	}()
	<-locked

	// 解绑：检查函数看到提升后的角色就拒绝（相当于目标的权限已经高过操作人）。
	rejected := fmt.Errorf("target has higher authority")
	var seen []uint32
	err = creds.DeleteMiniappBinding(ctx, uid, bindingID, MiniappUnbindGuard{Check: func(_ context.Context, roleIDs []uint32) error {
		seen = roleIDs
		for _, id := range roleIDs {
			if id == promotedRole {
				return rejected
			}
		}
		return nil
	}})
	require.NoError(t, <-promoted)
	require.Contains(t, seen, uint32(promotedRole), "解绑等到角色提升提交之后才读角色")
	require.ErrorIs(t, err, rejected)
	require.Equal(t, 1, client.UserCredential.Query().Where(usercredential.IDEQ(bindingID)).CountX(ctx), "绑定没有被删")
}

// 签发和解绑对同一个用户串行（审查第 3 条）：签发在锁里时解绑等着；
// 解绑提交之后，拿着同一个绑定 ID 的签发确认不通过，不执行。
func TestIssueUnderUserLock_SerialWithUnbindOnPostgres(t *testing.T) {
	entClient, ctx := openPostgresForTest(t)
	client := entClient.Client()
	creds := NewUserCredentialRepoForTest(entClient, password.NewBCryptCrypto(), nil)

	mobile := fmt.Sprintf("134%08d", os.Getpid()%100000000)
	uid := client.User.Create().SetUsername(fmt.Sprintf("issue-lock-%d", os.Getpid())).SetMobile(mobile).SaveX(ctx).ID
	t.Cleanup(func() {
		client.UserCredential.Delete().Where(usercredential.UserIDEQ(uid)).ExecX(ctx)
		client.User.Delete().Where(user.IDEQ(uid)).ExecX(ctx)
	})
	bindingID, err := creds.BindMiniapp(ctx, uid, fmt.Sprintf("openid-issue-lock-%d", os.Getpid()), MiniappBindCheck{Mobile: &mobile})
	require.NoError(t, err)

	inLock := make(chan struct{})
	release := make(chan struct{})
	issueDone := make(chan error, 1)
	var issuedAt, unboundAt time.Time
	go func() {
		issueDone <- creds.IssueUnderUserLock(ctx, uid, MiniappIssueGuard{BindingID: bindingID}, func() error {
			close(inLock)
			<-release
			issuedAt = time.Now()
			return nil
		})
	}()
	<-inLock

	unbindDone := make(chan error, 1)
	go func() {
		uerr := creds.DeleteMiniappBinding(ctx, uid, bindingID, MiniappUnbindGuard{})
		unboundAt = time.Now()
		unbindDone <- uerr
	}()
	select {
	case uerr := <-unbindDone:
		t.Fatalf("签发还在锁里，解绑不应该完成（err=%v）", uerr)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-issueDone)
	require.NoError(t, <-unbindDone)
	require.True(t, unboundAt.After(issuedAt), "解绑在签发之后完成")

	// 解绑已提交：在途的签发（手里还是旧的绑定 ID）确认不通过。
	called := false
	err = creds.IssueUnderUserLock(ctx, uid, MiniappIssueGuard{BindingID: bindingID}, func() error { called = true; return nil })
	require.ErrorIs(t, err, ErrMiniappBindingGone)
	require.False(t, called)
}
