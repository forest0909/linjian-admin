package data

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	entCrud "go-wind-admin/third_party/go-crud/entgo"
	"go-wind-admin/third_party/go-utils/password"
	"go-wind-admin/third_party/go-utils/trans"
	"go-wind-admin/third_party/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/third_party/kratos-bootstrap/logger"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"

	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/usercredential"
	"go-wind-admin/app/admin/service/internal/data/enttest"
)

type mobileWriteEnv struct {
	ctx       context.Context
	client    *ent.Client
	entClient *entCrud.EntClient[*ent.Client]
	users     UserRepo
	creds     *UserCredentialRepo
}

func newMobileWriteEnv(t *testing.T) *mobileWriteEnv {
	t.Helper()
	entClient := enttest.NewEntClientForTest(t)
	boot := bootstrap.NewContextWithParam(context.Background(), nil, nil, bLogger.NopLogger())
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &mobileWriteEnv{
		ctx:       enttest.NewSystemContext(context.Background()),
		client:    entClient.Client(),
		entClient: entClient,
		users: NewUserRepo(boot, entClient,
			NewUserRoleRepo(boot, entClient), NewUserOrgUnitRepo(boot, entClient), NewUserPositionRepo(boot, entClient), nil),
		creds: NewUserCredentialRepoForTest(entClient, password.NewBCryptCrypto(), NewConfigRepoForTest(entClient, rdb)),
	}
}

func (e *mobileWriteEnv) seedUser(t *testing.T, username, mobile string) uint32 {
	t.Helper()
	return e.client.User.Create().SetUsername(username).SetMobile(mobile).SetNickname("old-nick").SaveX(e.ctx).ID
}

func (e *mobileWriteEnv) user(t *testing.T, id uint32) *ent.User {
	t.Helper()
	return e.client.User.GetX(e.ctx, id)
}

// 管理员改号的五种请求，加上格式和占用检查。断言：guard 调没调、返回的去向、库里的值。
func TestUserRepoUpdateGuarded_MobileWrite(t *testing.T) {
	const oldMobile = "13800000001"

	cases := []struct {
		name         string
		data         *identityV1.User
		mask         []string
		wantChanged  bool
		wantMobile   string
		wantNickname string
	}{
		{
			name:        "带 mask 改号",
			data:        &identityV1.User{Mobile: trans.Ptr("13900000002")},
			mask:        []string{"mobile"},
			wantChanged: true, wantMobile: "13900000002", wantNickname: "old-nick",
		},
		{
			name:        "不带 mask 的全量请求改号",
			data:        &identityV1.User{Mobile: trans.Ptr("13900000002"), Nickname: trans.Ptr("new-nick")},
			wantChanged: true, wantMobile: "13900000002", wantNickname: "new-nick",
		},
		{
			name:        "带 mask 清空（值为空指针）",
			data:        &identityV1.User{},
			mask:        []string{"mobile"},
			wantChanged: true, wantMobile: "", wantNickname: "old-nick",
		},
		{
			name:        "不带 mask 清空（空串）",
			data:        &identityV1.User{Mobile: trans.Ptr("")},
			wantChanged: true, wantMobile: "", wantNickname: "old-nick",
		},
		{
			name:         "掩码原样回传不算变更",
			data:         &identityV1.User{Mobile: trans.Ptr("138****0001"), Nickname: trans.Ptr("new-nick")},
			mask:         []string{"mobile", "nickname"},
			wantMobile:   oldMobile,
			wantNickname: "new-nick",
		},
		{
			name:         "全量请求里掩码原样回传不算变更",
			data:         &identityV1.User{Mobile: trans.Ptr("138****0001"), Nickname: trans.Ptr("new-nick")},
			wantMobile:   oldMobile,
			wantNickname: "new-nick",
		},
		{
			name:       "号码没变",
			data:       &identityV1.User{Mobile: trans.Ptr(oldMobile), Nickname: trans.Ptr("new-nick")},
			mask:       []string{"mobile", "nickname"},
			wantMobile: oldMobile, wantNickname: "new-nick",
		},
		{
			name:       "带 +86 和空格的同一个号码不算变更",
			data:       &identityV1.User{Mobile: trans.Ptr(" +86" + oldMobile + " ")},
			mask:       []string{"mobile"},
			wantMobile: oldMobile, wantNickname: "old-nick",
		},
		{
			name:         "mask 里没有 mobile 时不写",
			data:         &identityV1.User{Mobile: trans.Ptr("13900000002"), Nickname: trans.Ptr("new-nick")},
			mask:         []string{"nickname"},
			wantMobile:   oldMobile,
			wantNickname: "new-nick",
		},
		{
			name:        "新号码收成 11 位纯数字再落库",
			data:        &identityV1.User{Mobile: trans.Ptr("+8613900000002")},
			mask:        []string{"mobile"},
			wantChanged: true, wantMobile: "13900000002", wantNickname: "old-nick",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newMobileWriteEnv(t)
			id := e.seedUser(t, "target", oldMobile)
			e.client.UserRole.Create().SetUserID(id).SetRoleID(77).SaveX(e.ctx)

			var guardCalls int
			req := &identityV1.UpdateUserRequest{Id: id, Data: tc.data}
			if tc.mask != nil {
				req.UpdateMask = &fieldmaskpb.FieldMask{Paths: tc.mask}
			}
			outcome, err := e.users.UpdateGuarded(e.ctx, req, &UserUpdateGuard{
				CheckCredentialWrite: func(_ context.Context, tx *ent.Tx, userID uint32, roleIDs []uint32) error {
					guardCalls++
					require.NotNil(t, tx)
					require.Equal(t, id, userID)
					require.Equal(t, []uint32{77}, roleIDs, "角色是锁内从库里读的")
					return nil
				},
			})
			require.NoError(t, err)
			require.Equal(t, tc.wantChanged, outcome.MobileChanged, "MobileChanged")
			if tc.wantChanged {
				require.Equal(t, 1, guardCalls, "号码变更要过 guard")
			} else {
				require.Zero(t, guardCalls, "没有变更不该调 guard")
			}

			got := e.user(t, id)
			require.Equal(t, tc.wantMobile, derefString(got.Mobile))
			require.Equal(t, tc.wantNickname, derefString(got.Nickname))
		})
	}

	t.Run("guard 拒绝时整个更新回滚", func(t *testing.T) {
		e := newMobileWriteEnv(t)
		id := e.seedUser(t, "target", oldMobile)
		denied := identityV1.ErrorForbidden("denied")
		_, err := e.users.UpdateGuarded(e.ctx, &identityV1.UpdateUserRequest{
			Id:   id,
			Data: &identityV1.User{Mobile: trans.Ptr("13900000002"), Nickname: trans.Ptr("new-nick")},
		}, &UserUpdateGuard{CheckCredentialWrite: func(context.Context, *ent.Tx, uint32, []uint32) error { return denied }})
		require.ErrorIs(t, err, denied)
		got := e.user(t, id)
		require.Equal(t, oldMobile, derefString(got.Mobile))
		require.Equal(t, "old-nick", derefString(got.Nickname), "同一请求里的其他字段也不能写")
	})

	t.Run("新号码不是中国大陆 11 位号码被拒", func(t *testing.T) {
		for _, bad := range []string{"12345", "+85212345678", "138 0000 0002"} {
			e := newMobileWriteEnv(t)
			id := e.seedUser(t, "target", oldMobile)
			_, err := e.users.UpdateGuarded(e.ctx, &identityV1.UpdateUserRequest{
				Id: id, Data: &identityV1.User{Mobile: trans.Ptr(bad)}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"mobile"}},
			}, nil)
			require.True(t, identityV1.IsBadRequest(err), "%q: %v", bad, err)
			require.Equal(t, oldMobile, derefString(e.user(t, id).Mobile))
		}
	})

	t.Run("新号码已被别的用户使用被拒", func(t *testing.T) {
		e := newMobileWriteEnv(t)
		id := e.seedUser(t, "target", oldMobile)
		e.seedUser(t, "other", "13900000002")
		_, err := e.users.UpdateGuarded(e.ctx, &identityV1.UpdateUserRequest{
			Id: id, Data: &identityV1.User{Mobile: trans.Ptr("13900000002"), Nickname: trans.Ptr("new-nick")},
		}, nil)
		require.True(t, identityV1.IsConflict(err), "got %v", err)
		got := e.user(t, id)
		require.Equal(t, oldMobile, derefString(got.Mobile))
		require.Equal(t, "old-nick", derefString(got.Nickname))
	})

	t.Run("两个用户都清空号码不算占用", func(t *testing.T) {
		e := newMobileWriteEnv(t)
		id := e.seedUser(t, "target", oldMobile)
		e.seedUser(t, "other", "")
		outcome, err := e.users.UpdateGuarded(e.ctx, &identityV1.UpdateUserRequest{
			Id: id, Data: &identityV1.User{}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"mobile"}},
		}, nil)
		require.NoError(t, err)
		require.True(t, outcome.MobileChanged)
	})

	t.Run("存量的不合规号码原样传回不报错", func(t *testing.T) {
		e := newMobileWriteEnv(t)
		id := e.seedUser(t, "target", "010-12345678")
		outcome, err := e.users.UpdateGuarded(e.ctx, &identityV1.UpdateUserRequest{
			Id: id, Data: &identityV1.User{Mobile: trans.Ptr("010-12345678"), Nickname: trans.Ptr("new-nick")},
		}, nil)
		require.NoError(t, err)
		require.False(t, outcome.MobileChanged)
		require.Equal(t, "010-12345678", derefString(e.user(t, id).Mobile))
	})
}

// 显式 mask 在仓储里被删空（掩码回传的邮箱是唯一路径）时，不能退化成全量更新。
func TestUserRepoUpdate_ExplicitMaskEmptiedIsNotFullUpdate(t *testing.T) {
	e := newMobileWriteEnv(t)
	id := e.seedUser(t, "target", "13800000001")
	require.NoError(t, e.client.User.UpdateOneID(id).SetEmail("real@example.com").Exec(e.ctx))

	data := func() *identityV1.User {
		return &identityV1.User{
			Email:    trans.Ptr("re***@example.com"), // 掩码回传
			Mobile:   trans.Ptr("13900000002"),
			Nickname: trans.Ptr("new-nick"),
			Status:   identityV1.User_DISABLED.Enum(),
		}
	}
	requireUnchanged := func() {
		t.Helper()
		got := e.user(t, id)
		require.Equal(t, "13800000001", derefString(got.Mobile))
		require.Equal(t, "real@example.com", derefString(got.Email))
		require.Equal(t, "old-nick", derefString(got.Nickname))
		require.NotEqual(t, "DISABLED", string(*got.Status))
	}

	// mask 只有 email，值是掩码：过滤后 mask 为空，什么用户字段都不写。
	outcome, err := e.users.UpdateGuarded(e.ctx, &identityV1.UpdateUserRequest{
		Id: id, Data: data(), UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"email"}},
	}, nil)
	require.NoError(t, err)
	require.False(t, outcome.MobileChanged)
	requireUnchanged()

	// mask 只有关联字段：同样不写用户字段，关联照常更新。
	_, err = e.users.UpdateGuarded(e.ctx, &identityV1.UpdateUserRequest{
		Id: id, Data: func() *identityV1.User { d := data(); d.RoleIds = []uint32{5}; return d }(),
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"role_ids"}},
	}, nil)
	require.NoError(t, err)
	requireUnchanged()
	roleIDs, err := e.users.ListRoleIDsByUserID(e.ctx, id)
	require.NoError(t, err)
	require.Equal(t, []uint32{5}, roleIDs)

	// 允许的字段照常写：mask 里除了掩码邮箱还有 nickname，只写 nickname。
	_, err = e.users.UpdateGuarded(e.ctx, &identityV1.UpdateUserRequest{
		Id: id, Data: data(), UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"email", "nickname"}},
	}, nil)
	require.NoError(t, err)
	got := e.user(t, id)
	require.Equal(t, "new-nick", derefString(got.Nickname))
	require.Equal(t, "13800000001", derefString(got.Mobile))
	require.Equal(t, "real@example.com", derefString(got.Email))

	// 目标不存在：404，不是静默成功。
	_, err = e.users.UpdateGuarded(e.ctx, &identityV1.UpdateUserRequest{
		Id: 99999, Data: data(), UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"email"}},
	}, nil)
	require.True(t, identityV1.IsNotFound(err), "got %v", err)
}

func (e *mobileWriteEnv) phoneCredentials(t *testing.T, userID uint32) []*ent.UserCredential {
	t.Helper()
	return e.client.UserCredential.Query().
		Where(usercredential.UserIDEQ(userID), usercredential.IdentityTypeEQ(usercredential.IdentityTypePhone)).
		AllX(e.ctx)
}

// 短信验证通过后的改号事务。
func TestBindVerifiedPhone(t *testing.T) {
	// 用普通用户的上下文调用：事务内部自己切到系统视图。
	userCtx := func(id uint32) context.Context {
		return enttest.NewSystemContext(context.Background())
	}

	t.Run("更新 mobile，只留一行 PHONE 凭证", func(t *testing.T) {
		e := newMobileWriteEnv(t)
		id := e.seedUser(t, "target", "13800000001")
		// 旧流程留下的两行 PHONE 凭证，外加一行用户名凭证（不能被动到）。
		for _, old := range []string{"13800000001", "13700000009"} {
			e.client.UserCredential.Create().SetUserID(id).SetIdentityType(usercredential.IdentityTypePhone).
				SetIdentifier(old).SetCredential("x").SaveX(e.ctx)
		}
		e.client.UserCredential.Create().SetUserID(id).SetIdentityType(usercredential.IdentityTypeUsername).
			SetIdentifier("target").SetCredential("x").SaveX(e.ctx)

		changed, err := e.creds.BindVerifiedPhone(userCtx(id), id, "13900000002")
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, "13900000002", derefString(e.user(t, id).Mobile))

		rows := e.phoneCredentials(t, id)
		require.Len(t, rows, 1)
		require.Equal(t, "13900000002", derefString(rows[0].Identifier))
		require.Equal(t, 1, e.client.UserCredential.Query().
			Where(usercredential.UserIDEQ(id), usercredential.IdentityTypeEQ(usercredential.IdentityTypeUsername)).CountX(e.ctx))
	})

	t.Run("号码被别人使用返回 CONFLICT，什么都没写", func(t *testing.T) {
		e := newMobileWriteEnv(t)
		id := e.seedUser(t, "target", "13800000001")
		e.seedUser(t, "other", "13900000002")
		changed, err := e.creds.BindVerifiedPhone(userCtx(id), id, "13900000002")
		require.True(t, authenticationV1.IsConflict(err), "got %v", err)
		require.False(t, changed)
		require.Equal(t, "13800000001", derefString(e.user(t, id).Mobile))
		require.Empty(t, e.phoneCredentials(t, id))
	})

	t.Run("新旧号码相同时什么都不做", func(t *testing.T) {
		e := newMobileWriteEnv(t)
		id := e.seedUser(t, "target", "13800000001")
		before := e.user(t, id).UpdatedAt
		changed, err := e.creds.BindVerifiedPhone(userCtx(id), id, "13800000001")
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, before, e.user(t, id).UpdatedAt)
		require.Empty(t, e.phoneCredentials(t, id))
	})

	t.Run("用户不存在", func(t *testing.T) {
		e := newMobileWriteEnv(t)
		_, err := e.creds.BindVerifiedPhone(context.Background(), 999, "13800000001")
		require.True(t, authenticationV1.IsNotFound(err), "got %v", err)
	})
}

func newVCodeCacheEnv(t *testing.T) (*VCodeCache, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewVCodeCacheForTest(rdb), mr, rdb
}

func TestVCodeCache_ForUser(t *testing.T) {
	ctx := context.Background()
	const purpose, channel, contact, code = "bind_contact", "phone", "13800000001", "123456"

	t.Run("别的用户申请的验证码不能用", func(t *testing.T) {
		c, _, _ := newVCodeCacheEnv(t)
		require.NoError(t, c.SaveForUser(ctx, 1, purpose, channel, contact, code, time.Minute))
		ok, err := c.VerifyForUser(ctx, 2, purpose, channel, contact, code)
		require.NoError(t, err)
		require.False(t, ok)
		// 别人试错不影响本人的验证码。
		ok, err = c.VerifyForUser(ctx, 1, purpose, channel, contact, code)
		require.NoError(t, err)
		require.True(t, ok)
	})

	t.Run("换一个联系方式不能用", func(t *testing.T) {
		c, _, _ := newVCodeCacheEnv(t)
		require.NoError(t, c.SaveForUser(ctx, 1, purpose, channel, contact, code, time.Minute))
		ok, err := c.VerifyForUser(ctx, 1, purpose, channel, "13900000002", code)
		require.NoError(t, err)
		require.False(t, ok)
	})

	t.Run("换一个渠道不能用", func(t *testing.T) {
		c, _, _ := newVCodeCacheEnv(t)
		// 邮件渠道为同一段文本签发的验证码，不能过短信渠道的验证。
		require.NoError(t, c.SaveForUser(ctx, 1, purpose, "email", contact, code, time.Minute))
		ok, err := c.VerifyForUser(ctx, 1, purpose, "phone", contact, code)
		require.NoError(t, err)
		require.False(t, ok)
		ok, err = c.VerifyForUser(ctx, 1, purpose, "email", contact, code)
		require.NoError(t, err)
		require.True(t, ok)
	})

	t.Run("用过一次就作废", func(t *testing.T) {
		c, _, _ := newVCodeCacheEnv(t)
		require.NoError(t, c.SaveForUser(ctx, 1, purpose, channel, contact, code, time.Minute))
		ok, _ := c.VerifyForUser(ctx, 1, purpose, channel, contact, code)
		require.True(t, ok)
		ok, _ = c.VerifyForUser(ctx, 1, purpose, channel, contact, code)
		require.False(t, ok)
	})

	t.Run("错 5 次后正确的也不行", func(t *testing.T) {
		c, _, _ := newVCodeCacheEnv(t)
		require.NoError(t, c.SaveForUser(ctx, 1, purpose, channel, contact, code, time.Minute))
		for i := 0; i < VCodeMaxAttempts; i++ {
			ok, err := c.VerifyForUser(ctx, 1, purpose, channel, contact, "000000")
			require.NoError(t, err)
			require.False(t, ok)
		}
		ok, err := c.VerifyForUser(ctx, 1, purpose, channel, contact, code)
		require.NoError(t, err)
		require.False(t, ok)
	})

	t.Run("错 4 次后正确的还行，重新申请清零计数", func(t *testing.T) {
		c, _, _ := newVCodeCacheEnv(t)
		require.NoError(t, c.SaveForUser(ctx, 1, purpose, channel, contact, code, time.Minute))
		for i := 0; i < VCodeMaxAttempts-1; i++ {
			_, _ = c.VerifyForUser(ctx, 1, purpose, channel, contact, "000000")
		}
		require.NoError(t, c.SaveForUser(ctx, 1, purpose, channel, contact, "654321", time.Minute))
		for i := 0; i < VCodeMaxAttempts-1; i++ {
			_, _ = c.VerifyForUser(ctx, 1, purpose, channel, contact, "000000")
		}
		ok, err := c.VerifyForUser(ctx, 1, purpose, channel, contact, "654321")
		require.NoError(t, err)
		require.True(t, ok)
	})

	t.Run("过期", func(t *testing.T) {
		c, mr, _ := newVCodeCacheEnv(t)
		require.NoError(t, c.SaveForUser(ctx, 1, purpose, channel, contact, code, time.Minute))
		mr.FastForward(2 * time.Minute)
		ok, err := c.VerifyForUser(ctx, 1, purpose, channel, contact, code)
		require.NoError(t, err)
		require.False(t, ok)
	})

	t.Run("并发提交同一个正确验证码只有一个成功", func(t *testing.T) {
		c, _, _ := newVCodeCacheEnv(t)
		require.NoError(t, c.SaveForUser(ctx, 1, purpose, channel, contact, code, time.Minute))
		var wins int32
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if ok, err := c.VerifyForUser(ctx, 1, purpose, channel, contact, code); err == nil && ok {
					atomic.AddInt32(&wins, 1)
				}
			}()
		}
		wg.Wait()
		require.Equal(t, int32(1), wins)
	})

	t.Run("Redis 出错时报错，不放行", func(t *testing.T) {
		c, mr, _ := newVCodeCacheEnv(t)
		require.NoError(t, c.SaveForUser(ctx, 1, purpose, channel, contact, code, time.Minute))
		mr.Close()
		ok, err := c.VerifyForUser(ctx, 1, purpose, channel, contact, code)
		require.Error(t, err)
		require.False(t, ok)
		allowed, err := c.AllowSendForUser(ctx, 1, channel, contact)
		require.Error(t, err)
		require.False(t, allowed)
		require.Error(t, c.SaveForUser(ctx, 1, purpose, channel, contact, code, time.Minute))
	})
}

func TestVCodeCache_SendQuota(t *testing.T) {
	ctx := context.Background()

	allow := func(c *VCodeCache, uid uint32, contact string) bool {
		ok, err := c.AllowSendForUser(ctx, uid, "phone", contact)
		require.NoError(t, err)
		return ok
	}

	t.Run("同一号码每分钟 1 次", func(t *testing.T) {
		c, mr, _ := newVCodeCacheEnv(t)
		require.True(t, allow(c, 1, "13800000001"))
		require.False(t, allow(c, 1, "13800000001"))
		// 换一个用户发同一个号码也算。
		require.False(t, allow(c, 2, "13800000001"))
		mr.FastForward(61 * time.Second)
		require.True(t, allow(c, 1, "13800000001"))
	})

	t.Run("同一用户每小时 5 次", func(t *testing.T) {
		c, mr, _ := newVCodeCacheEnv(t)
		contacts := []string{"13800000001", "13800000002", "13800000003", "13800000004", "13800000005", "13800000006"}
		for i := 0; i < 5; i++ {
			require.True(t, allow(c, 1, contacts[i]), "第 %d 次", i+1)
		}
		require.False(t, allow(c, 1, contacts[5]))
		// 被用户额度拦住的请求不占号码额度：别的用户可以发这个号码。
		require.True(t, allow(c, 2, contacts[5]))
		mr.FastForward(time.Hour + time.Second)
		require.True(t, allow(c, 1, "13800000007"))
	})

	t.Run("邮件请求不占号码额度，用户合计额度两个渠道共用", func(t *testing.T) {
		c, _, _ := newVCodeCacheEnv(t)
		ok, err := c.AllowSendForUser(ctx, 1, "email", "13800000001")
		require.NoError(t, err)
		require.True(t, ok)
		require.True(t, allow(c, 2, "13800000001"), "同一段文本在短信渠道的额度没被邮件请求占掉")
		// 用户 1 已用 1 次（邮件），再发 4 次短信后合计 5 次，第 6 次被拦。
		for i := 2; i <= 5; i++ {
			require.True(t, allow(c, 1, "1380000000"+string(rune('0'+i))))
		}
		require.False(t, allow(c, 1, "13800000009"))
	})

	t.Run("重新保存验证码不会清掉额度", func(t *testing.T) {
		c, _, _ := newVCodeCacheEnv(t)
		require.True(t, allow(c, 1, "13800000001"))
		require.NoError(t, c.SaveForUser(ctx, 1, "bind_contact", "phone", "13800000001", "111111", time.Minute))
		require.NoError(t, c.SaveForUser(ctx, 1, "bind_contact", "phone", "13800000001", "222222", time.Minute))
		require.False(t, allow(c, 1, "13800000001"))
	})
}
