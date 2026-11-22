package service

import (
	"context"
	"errors"

	kratosErrors "github.com/go-kratos/kratos/v2/errors"

	"go-wind-admin/third_party/go-utils/trans"
	"go-wind-admin/third_party/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/third_party/kratos-bootstrap/logger"

	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"

	"go-wind-admin/app/admin/service/internal/data"
)

// BindingRevoker 小程序统一的下线函数。下线只有一种做法：
//
//	revokeBinding(userID, bindingID)
//	  1. 删除绑定行（条件限定用户、绑定 ID、provider、租户；删除 0 行也算成功）；
//	  2. 吊销该用户的全部 app 令牌：推进撤销版本，删除 app 令牌和会话元数据。
//
// 四个入口都调它：小程序退出、自助踢会话、管理员强制下线（目标是 app 会话时）、管理端解绑。
// 结果：被下线的那个微信令牌立刻失效，续期被拒（绑定没了），必须重新登录；
// 同一用户的其他微信令牌也失效，但各自的绑定还在，下一次请求静默续期；
// 管理端会话和 SSE 连接不受影响。
//
// 操作人动别人的绑定（管理端解绑、管理员强制下线）时，删除事务里锁住目标用户行之后
// 复核数据范围和权限上限；用户下线自己的会话不复核。
//
// 先提交数据库，再动 Redis。Redis 失败时返回 503：绑定已删，之后不能再续期；
// 操作是幂等的，再调一次即可把没清掉的令牌清掉。全量吊销没做完时会话元数据留着
// （见 UserTokenCache.RevokeToken），按会话下线的入口重试时还能从它找到绑定，再走一遍全量吊销。
type BindingRevoker struct {
	log                *bLogger.Helper
	userCredentialRepo *data.UserCredentialRepo
	authenticator      *data.Authenticator
	// 权限上限检查要用。
	userRepo data.UserRepo
	roleRepo *data.RoleRepo
}

func NewBindingRevoker(ctx *bootstrap.Context, userCredentialRepo *data.UserCredentialRepo, authenticator *data.Authenticator,
	userRepo data.UserRepo, roleRepo *data.RoleRepo) *BindingRevoker {
	return &BindingRevoker{
		log:                ctx.NewLoggerHelper("miniapp-revoke/service/admin-service"),
		userCredentialRepo: userCredentialRepo,
		authenticator:      authenticator,
		userRepo:           userRepo,
		roleRepo:           roleRepo,
	}
}

// unbindGuard 操作人动 userID 的绑定时要复核的东西。operator 为 nil 或就是本人时不复核。
func (r *BindingRevoker) unbindGuard(operator *authenticationV1.UserTokenPayload, userID uint32) data.MiniappUnbindGuard {
	if operator == nil || operator.GetUserId() == userID {
		return data.MiniappUnbindGuard{}
	}
	return data.MiniappUnbindGuard{
		EnforceDataScope: true,
		// 权限上限：不能动权限比自己高的账号的登录凭证。roleIDs 是锁内读到的目标用户当前角色。
		Check: func(ctx context.Context, roleIDs []uint32) error {
			return ensureTargetWithinOperator(ctx, r.roleRepo, r.userRepo.ListRoleIDsByUserID, operator, userID, roleIDs)
		},
	}
}

// RevokeBinding 删除绑定，再吊销该用户的全部 app 令牌。
// operator 是动别人绑定的操作人；用户下线自己的会话传 nil。
// 目标在操作人的数据范围外返回 404，权限比操作人高返回 403，两种情况都不删绑定、不吊销令牌。
func (r *BindingRevoker) RevokeBinding(ctx context.Context, operator *authenticationV1.UserTokenPayload, userID, bindingID uint32) error {
	if err := r.userCredentialRepo.DeleteMiniappBinding(ctx, userID, bindingID, r.unbindGuard(operator, userID)); err != nil {
		if errors.Is(err, data.ErrMiniappTargetNotFound) {
			return adminV1.ErrorNotFound("user not found")
		}
		// 权限检查给出的错误（403 等）原样返回。
		var ke *kratosErrors.Error
		if errors.As(err, &ke) {
			r.log.Warnf(ctx, "operator [%d] unbind miniapp of user [%d] rejected: %v", operator.GetUserId(), userID, err)
			return err
		}
		return authenticationV1.ErrorServiceUnavailable("unbind failed, please retry")
	}
	if err := r.authenticator.RevokeUserToken(ctx, authenticationV1.ClientType_app, userID); err != nil {
		// 绑定已经删了，但 app 令牌没清干净：已签发的令牌在自己的有效期内还能用。再调一次即可。
		r.log.Errorf(ctx, "revoke app tokens of user [%d] after unbinding [%d] failed: %v", userID, bindingID, err)
		return authenticationV1.ErrorServiceUnavailable("binding removed but session revocation failed, please retry")
	}
	return nil
}

// RevokeAppSession 下线一个 app 会话：会话记着绑定就走 RevokeBinding；
// 没有绑定（H5 登录，或登录时没建成绑定）只按 JTI 吊销这一个会话，不删任何绑定。
// operator 的含义同 RevokeBinding。
func (r *BindingRevoker) RevokeAppSession(ctx context.Context, operator *authenticationV1.UserTokenPayload, userID uint32, jti string) error {
	meta, err := r.authenticator.GetSessionMeta(ctx, authenticationV1.ClientType_app, userID, jti)
	if err != nil {
		r.log.Errorf(ctx, "get app session meta failed for user [%d]: %v", userID, err)
		return authenticationV1.ErrorServiceUnavailable("session store unavailable")
	}
	if meta != nil && meta.BindingID != 0 {
		return r.RevokeBinding(ctx, operator, userID, meta.BindingID)
	}
	if err = r.authenticator.RevokeTokenByJti(ctx, trans.Ptr(authenticationV1.ClientType_app), userID, jti); err != nil {
		r.log.Errorf(ctx, "revoke app session of user [%d] failed: %v", userID, err)
		return authenticationV1.ErrorServiceUnavailable("session revocation failed, please retry")
	}
	return nil
}
