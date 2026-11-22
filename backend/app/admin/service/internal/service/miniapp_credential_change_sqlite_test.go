package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"go-wind-admin/third_party/go-utils/trans"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"

	"go-wind-admin/pkg/middleware/auth"
)

// 手机号和密码是登录凭证：改号、清号、改密、重置密码都在同一个事务里删掉该用户的全部微信绑定，
// 提交后吊销 app 令牌。号码没有实际变化时什么都不做。事务回滚时绑定还在。

const (
	changeMobile = "13800000001"
	newPassword  = "Brand#New9Pass"
)

// boundUser 一个已经用两个微信登录过的用户。
type boundUser struct {
	*miniappEnv
	uid     uint32
	adminID uint32
	tokens  []string
}

func newBoundUser(t *testing.T) *boundUser {
	e := newMiniappEnv(t)
	b := &boundUser{miniappEnv: e, uid: e.seedUser("zhangsan", changeMobile)}
	for i, openid := range []string{"openid-change-AAAA", "openid-change-BBBB"} {
		resp, err := e.phoneLogin(fmt.Sprintf("10.0.0.%d", i+1), openid, changeMobile)
		require.NoError(t, err)
		b.tokens = append(b.tokens, resp.GetAccessToken())
	}
	require.Len(t, e.bindings(b.uid), 2)
	return b
}

// requireUnbound 绑定全部删掉，app 会话失效，两个微信都续不上。
func (b *boundUser) requireUnbound(t *testing.T, msg string) {
	t.Helper()
	require.Empty(t, b.bindings(b.uid), msg)
	for _, token := range b.tokens {
		require.False(t, b.tokenValid(token), msg)
	}
	_, err := b.silentLogin("10.0.0.1", "openid-change-AAAA")
	requireReason(t, err, 403, "MINIAPP_NOT_BOUND", msg)
}

// requireStillBound 什么都没动：两条绑定还在，app 会话有效。
func (b *boundUser) requireStillBound(t *testing.T, msg string) {
	t.Helper()
	require.Len(t, b.bindings(b.uid), 2, msg)
	for _, token := range b.tokens {
		require.True(t, b.tokenValid(token), msg)
	}
}

// adminCtx 平台管理员的请求上下文。操作人是一个真实存在的用户（更新用户时服务会先读操作人自己的行）。
func (b *boundUser) adminCtx() context.Context {
	if b.adminID == 0 {
		b.adminID = b.seedUser("root-admin", "13600000000")
	}
	return opCtx(b.sys, b.adminID, 0, true)
}

// selfCtx 用户本人在管理端的请求上下文（自助改密按令牌里的用户名找凭证）。
func (b *boundUser) selfCtx() context.Context {
	return auth.NewContext(b.sys, &authenticationV1.UserTokenPayload{UserId: b.uid, Username: trans.Ptr("zhangsan")})
}

func (b *boundUser) mobile() string {
	u := b.client.User.GetX(b.sys, b.uid)
	if u.Mobile == nil {
		return ""
	}
	return *u.Mobile
}

// ---- 自助改号（短信验证码）----

func TestVerifyContact_PhoneChangeDeletesBindings(t *testing.T) {
	b := newBoundUser(t)
	self := b.selfCtx()

	// 新旧号码相同：直接成功，什么都不做，不删绑定。
	_, err := b.profile.BindContact(self, bindPhoneReq(changeMobile))
	require.NoError(t, err)
	_, err = b.profile.VerifyContact(self, verifyPhoneReq(changeMobile, b.sender.sms[changeMobile]))
	require.NoError(t, err)
	b.requireStillBound(t, "号码没变")

	// 号码被别人使用：CONFLICT，什么都没动。
	b.seedUser("other", "13900000002")
	_, err = b.profile.BindContact(self, bindPhoneReq("13900000002"))
	require.NoError(t, err)
	_, err = b.profile.VerifyContact(self, verifyPhoneReq("13900000002", b.sender.sms["13900000002"]))
	requireReason(t, err, 409, "CONFLICT")
	b.requireStillBound(t, "号码被占用")
	require.Equal(t, changeMobile, b.mobile())

	// 换成新号码：更新 mobile，删绑定，app 会话失效；管理端会话不受影响。
	b.mr.FastForward(61 * time.Second)
	_, err = b.profile.BindContact(self, bindPhoneReq("13900000003"))
	require.NoError(t, err)
	b.revokedTypes()
	_, err = b.profile.VerifyContact(self, verifyPhoneReq("13900000003", b.sender.sms["13900000003"]))
	require.NoError(t, err)
	require.Equal(t, "13900000003", b.mobile())
	b.requireUnbound(t, "自助改号")
	require.Equal(t, []authenticationV1.ClientType{authenticationV1.ClientType_app}, b.revokedTypes(), "只吊销 app 令牌")

	// 旧号码不能再一键登录到这个账号，新号码可以。
	_, err = b.phoneLogin("10.0.0.1", "openid-change-AAAA", changeMobile)
	requireReason(t, err, 403, "MINIAPP_PHONE_NOT_REGISTERED")
	resp, err := b.phoneLogin("10.0.0.1", "openid-change-AAAA", "13900000003")
	require.NoError(t, err)
	require.True(t, resp.GetMiniappBound())
}

// ---- 管理员改号的五种请求 ----

func TestAdminMobileChange_DeletesBindingsOnlyOnRealChange(t *testing.T) {
	cases := []struct {
		name       string
		req        func(uid uint32) *identityV1.UpdateUserRequest
		changed    bool
		wantMobile string
	}{
		{
			name: "带 mask 改号",
			req: func(uid uint32) *identityV1.UpdateUserRequest {
				return &identityV1.UpdateUserRequest{Id: uid, Data: &identityV1.User{Mobile: trans.Ptr("13900000009")},
					UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"mobile"}}}
			},
			changed: true, wantMobile: "13900000009",
		},
		{
			name: "不带 mask 的全量请求改号",
			req: func(uid uint32) *identityV1.UpdateUserRequest {
				return &identityV1.UpdateUserRequest{Id: uid, Data: &identityV1.User{Mobile: trans.Ptr("13900000009"), Nickname: trans.Ptr("n")}}
			},
			changed: true, wantMobile: "13900000009",
		},
		{
			name: "显式清空",
			req: func(uid uint32) *identityV1.UpdateUserRequest {
				return &identityV1.UpdateUserRequest{Id: uid, Data: &identityV1.User{},
					UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"mobile"}}}
			},
			changed: true, wantMobile: "",
		},
		{
			name: "掩码原样回传",
			req: func(uid uint32) *identityV1.UpdateUserRequest {
				return &identityV1.UpdateUserRequest{Id: uid, Data: &identityV1.User{Mobile: trans.Ptr("138****0001"), Nickname: trans.Ptr("n")},
					UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"mobile", "nickname"}}}
			},
			changed: false, wantMobile: changeMobile,
		},
		{
			name: "号码没变",
			req: func(uid uint32) *identityV1.UpdateUserRequest {
				return &identityV1.UpdateUserRequest{Id: uid, Data: &identityV1.User{Mobile: trans.Ptr(changeMobile), Nickname: trans.Ptr("n")},
					UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"mobile", "nickname"}}}
			},
			changed: false, wantMobile: changeMobile,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newBoundUser(t)
			adminToken := b.adminToken(b.uid)
			b.revokedTypes()

			req := c.req(b.uid)
			// 全量请求会重建角色：带上原有的角色，避免把「角色变了」的吊销混进来。
			req.Data.RoleIds = []uint32{b.roleID}
			_, err := b.users.Update(b.adminCtx(), req)
			require.NoError(t, err)
			require.Equal(t, c.wantMobile, b.mobile())

			if c.changed {
				b.requireUnbound(t, c.name)
				require.Equal(t, []authenticationV1.ClientType{authenticationV1.ClientType_app}, b.revokedTypes())
			} else {
				b.requireStillBound(t, c.name)
				require.Empty(t, b.revokedTypes())
			}
			require.True(t, b.tokenValid(adminToken), "管理端会话不受改号影响")
		})
	}
}

// ---- 改密、重置密码、更新用户时附带改密 ----

func TestPasswordChange_DeletesBindingsInSameTransaction(t *testing.T) {
	t.Run("自助改密", func(t *testing.T) {
		b := newBoundUser(t)
		self := b.selfCtx()

		// 旧密码不对：事务回滚，绑定还在。
		_, err := b.profile.ChangePassword(self, &identityV1.ChangePasswordRequest{
			OldPassword: encryptLoginPassword(t, "Wrong@Pass1"), NewPassword: encryptLoginPassword(t, newPassword),
		})
		require.Error(t, err)
		b.requireStillBound(t, "旧密码不对")
		// 新密码不合规：同样回滚。
		_, err = b.profile.ChangePassword(self, &identityV1.ChangePasswordRequest{
			OldPassword: encryptLoginPassword(t, defaultPassword), NewPassword: encryptLoginPassword(t, "short"),
		})
		require.Error(t, err)
		b.requireStillBound(t, "新密码不合规")

		_, err = b.profile.ChangePassword(self, &identityV1.ChangePasswordRequest{
			OldPassword: encryptLoginPassword(t, defaultPassword), NewPassword: encryptLoginPassword(t, newPassword),
		})
		require.NoError(t, err)
		b.requireUnbound(t, "自助改密")
	})

	t.Run("管理员重置密码", func(t *testing.T) {
		b := newBoundUser(t)
		// 新密码不合规：回滚，绑定还在。
		admin := b.adminCtx()
		_, err := b.users.EditUserPassword(admin, &identityV1.EditUserPasswordRequest{UserId: b.uid, NewPassword: encryptLoginPassword(t, "short")})
		require.Error(t, err)
		b.requireStillBound(t, "新密码不合规")

		_, err = b.users.EditUserPassword(admin, &identityV1.EditUserPasswordRequest{UserId: b.uid, NewPassword: encryptLoginPassword(t, newPassword)})
		require.NoError(t, err)
		b.requireUnbound(t, "管理员重置密码")
	})

	t.Run("更新用户时附带改密", func(t *testing.T) {
		b := newBoundUser(t)
		update := func(pw string) error {
			_, err := b.users.Update(b.adminCtx(), &identityV1.UpdateUserRequest{
				Id:         b.uid,
				Data:       &identityV1.User{Nickname: trans.Ptr("renamed"), RoleIds: []uint32{b.roleID}},
				UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"nickname"}},
				Password:   trans.Ptr(encryptLoginPassword(t, pw)),
			})
			return err
		}
		// 新密码不合规：整个更新回滚，昵称没改，绑定还在。
		requireReason(t, update("short"), 400, "BAD_REQUEST")
		require.Contains(t, update("short").Error(), "password", "被拒的原因是口令不合规")
		b.requireStillBound(t, "新密码不合规")
		require.Equal(t, "zhangsan", *b.client.User.GetX(b.sys, b.uid).Nickname)

		require.NoError(t, update(newPassword))
		b.requireUnbound(t, "更新用户时附带改密")
		require.Equal(t, "renamed", *b.client.User.GetX(b.sys, b.uid).Nickname)
	})
}

// 自助改密后 Redis 吊销失败：返回 503；密码已经改了，绑定已经删了，之后续不上。
func TestPasswordChange_RevokeFailureReturns503(t *testing.T) {
	b := newBoundUser(t)
	self := b.selfCtx()

	b.fault.set(func(name string, args []string) bool {
		return name == "incr" && len(args) > 1 && args[1] == fmt.Sprintf("rv:1:%d", b.uid)
	})
	_, err := b.profile.ChangePassword(self, &identityV1.ChangePasswordRequest{
		OldPassword: encryptLoginPassword(t, defaultPassword), NewPassword: encryptLoginPassword(t, newPassword),
	})
	requireReason(t, err, 503, "SERVICE_UNAVAILABLE")
	b.fault.set(nil)

	require.Empty(t, b.bindings(b.uid), "数据库部分已经提交")
	_, err = b.passwordLogin("10.0.0.1", "zhangsan", defaultPassword, "")
	requireReason(t, err, 400, "INVALID_PASSWORD")
	_, err = b.silentLogin("10.0.0.1", "openid-change-AAAA")
	requireReason(t, err, 403, "MINIAPP_NOT_BOUND")
	_, err = b.passwordLogin("10.0.0.1", "zhangsan", newPassword, "")
	require.NoError(t, err)

	// 管理员重置密码同理。
	b2 := newBoundUser(t)
	b2.fault.set(func(name string, args []string) bool {
		return name == "incr" && len(args) > 1 && args[1] == fmt.Sprintf("rv:1:%d", b2.uid)
	})
	_, err = b2.users.EditUserPassword(b2.adminCtx(), &identityV1.EditUserPasswordRequest{UserId: b2.uid, NewPassword: encryptLoginPassword(t, newPassword)})
	requireReason(t, err, 503, "SERVICE_UNAVAILABLE")
	require.Empty(t, b2.bindings(b2.uid))

	// 操作可以重复执行：原样再调一次成功（口令历史只记旧口令，不拦同一个新密码）。
	b2.fault.set(nil)
	_, err = b2.users.EditUserPassword(b2.adminCtx(), &identityV1.EditUserPasswordRequest{UserId: b2.uid, NewPassword: encryptLoginPassword(t, newPassword)})
	require.NoError(t, err)
	require.False(t, b2.tokenValid(b2.tokens[0]))
	_, err = b2.passwordLogin("10.0.0.1", "zhangsan", newPassword, "")
	require.NoError(t, err)
}
