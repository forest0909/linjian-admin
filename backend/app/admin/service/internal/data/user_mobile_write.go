package data

import (
	"context"
	"slices"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	"go-wind-admin/third_party/go-crud/viewer"

	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/ent/usercredential"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"

	"go-wind-admin/pkg/phone"
)

// 手机号是登录凭证（小程序一键登录按 sys_users.mobile 精确匹配），写它按写凭证来管。
// 本文件是两条写入路径在仓储事务里的公共部分：管理员改号（userRepo.UpdateGuarded）
// 和自助短信改号（UserCredentialRepo.BindVerifiedPhone）。

// MobileChangeGuard 管理员更新用户时，号码确实要变（含清空）才会调用。
// 在更新事务里、任何写入之前调用；返回错误则整个更新回滚。
// newMobile 是规范后的 11 位号码，清空时为空串。
type MobileChangeGuard func(ctx context.Context, tx *ent.Tx, userID uint32, newMobile string) error

// UserUpdateOutcome 一次用户更新里手机号的实际去向。
type UserUpdateOutcome struct {
	// MobileChanged 处理完字段屏蔽、mask、掩码回传之后，要写的号码和库里的不一样（含清空）。
	MobileChanged bool
}

// lockUserRow 在事务里锁住并读出用户行（SELECT ... FOR UPDATE）。
// 凭证变更的事务都先锁这一行，对同一个用户串行。SQLite 没有行锁，写事务本身就是串行的。
func lockUserRow(ctx context.Context, tx *ent.Tx, dialectName string, userID uint32) (*ent.User, error) {
	q := tx.User.Query().Where(user.IDEQ(userID))
	if dialectName != dialect.SQLite {
		q = q.ForUpdate()
	}
	return q.Only(ctx)
}

// mobileUsedByOthers 租户 0 里有没有别的用户在用这个号码。用系统视图查，不受操作人数据范围影响。
func mobileUsedByOthers(ctx context.Context, tx *ent.Tx, userID uint32, mobile string) (bool, error) {
	return tx.User.Query().
		Where(
			user.MobileEQ(mobile),
			user.IDNEQ(userID),
			user.Or(user.TenantIDEQ(0), user.TenantIDIsNil()),
		).
		Exist(viewer.WithSystemContext(ctx))
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// resolveMobileWrite 算出这次更新对 mobile 的实际效果，在字段屏蔽、mask 过滤、掩码回传都处理完之后调用。
// 规则和 UpdateOne 的落库行为一致：没有 mask 路径时写 Data 里非空指针的字段；
// 有 mask 路径时只写路径里的字段，值为空指针表示清空。
func resolveMobileWrite(req *identityV1.UpdateUserRequest) (writes bool, value string) {
	paths := req.GetUpdateMask().GetPaths()
	if len(paths) == 0 {
		writes = req.GetData().Mobile != nil
	} else {
		writes = slices.Contains(paths, "mobile")
	}
	if !writes {
		return false, ""
	}
	return true, strings.TrimSpace(req.GetData().GetMobile())
}

// guardMobileWrite 管理员更新用户的事务里、写入之前调用。
// 要写 mobile 时锁用户行、和当前值比较；确实变了才调 guard，并检查新号码没有别的用户在用。
// 写入值会换成规范后的号码。
func (r *userRepo) guardMobileWrite(ctx context.Context, tx *ent.Tx, req *identityV1.UpdateUserRequest, guard MobileChangeGuard) (UserUpdateOutcome, error) {
	var outcome UserUpdateOutcome

	writes, newMobile := resolveMobileWrite(req)
	if !writes {
		return outcome, nil
	}

	current, err := lockUserRow(ctx, tx, r.entClient.Driver().Dialect(), req.GetId())
	if err != nil {
		return outcome, mapUserWriteError(r.log, ctx, "lock user", err)
	}
	currentMobile := derefString(current.Mobile)

	// 原样传回库里的值不算变更，也不重新校验格式（存量数据里可能有不合规的号码）。
	if newMobile != currentMobile && newMobile != "" {
		normalized, ok := phone.NormalizeCNMobile(newMobile)
		if !ok {
			return outcome, identityV1.ErrorBadRequest("invalid mobile number")
		}
		newMobile = normalized
	}
	if req.Data.Mobile != nil {
		req.Data.Mobile = &newMobile
	}
	if newMobile == currentMobile {
		return outcome, nil
	}

	outcome.MobileChanged = true
	if guard != nil {
		if err = guard(ctx, tx, req.GetId(), newMobile); err != nil {
			return outcome, err
		}
	}
	if newMobile != "" {
		used, uerr := mobileUsedByOthers(ctx, tx, req.GetId(), newMobile)
		if uerr != nil {
			r.log.Errorf(ctx, "check mobile in use failed: %s", uerr.Error())
			return outcome, identityV1.ErrorInternalServerError("check mobile failed")
		}
		if used {
			return outcome, identityV1.ErrorConflict("mobile already in use")
		}
	}
	return outcome, nil
}

// phoneCredentialPlaceholder 合法的 bcrypt 占位值：PHONE 凭证只记录「这个号码验证过」，不用于密码校验。
const phoneCredentialPlaceholder = "$2a$10$1sbpKmhQDpXLHnDnEQ1nLe3oOnYyP2bUJyqHcX2T0Fq1qfyoXOrPm"

// BindVerifiedPhone 短信验证通过后把手机号写成用户的登录号码。一个事务里：
// 锁用户行；号码被别的用户使用时返回 CONFLICT；更新 sys_users.mobile；
// 删掉该用户原有的 PHONE 凭证行再插入新的（每个用户最多一行）。
// mobile 必须是规范后的 11 位号码。新旧号码相同时什么都不写，changed 为 false。
func (r *UserCredentialRepo) BindVerifiedPhone(ctx context.Context, userID uint32, mobile string) (changed bool, err error) {
	// userID 来自已验证的令牌，事务只动这个用户自己的行，用系统视图执行。
	ctx = viewer.WithSystemContext(ctx)

	tx, err := r.entClient.Client().Tx(ctx)
	if err != nil {
		r.log.Errorf(ctx, "start transaction failed: %s", err.Error())
		return false, authenticationV1.ErrorInternalServerError("start transaction failed")
	}
	var txCommitted bool
	defer finishEntTx(tx, &txCommitted, func(rollbackErr error) {
		r.log.Errorf(ctx, "transaction rollback failed: %s", rollbackErr.Error())
	})

	current, err := lockUserRow(ctx, tx, r.entClient.Driver().Dialect(), userID)
	if err != nil {
		if ent.IsNotFound(err) {
			return false, authenticationV1.ErrorNotFound("user not found")
		}
		r.log.Errorf(ctx, "lock user [%d] failed: %s", userID, err.Error())
		return false, authenticationV1.ErrorInternalServerError("query user failed")
	}
	if derefString(current.Mobile) == mobile {
		return false, nil
	}

	used, err := mobileUsedByOthers(ctx, tx, userID, mobile)
	if err != nil {
		r.log.Errorf(ctx, "check mobile in use failed: %s", err.Error())
		return false, authenticationV1.ErrorInternalServerError("check mobile failed")
	}
	if used {
		return false, authenticationV1.ErrorConflict("mobile already in use")
	}

	if err = tx.User.UpdateOneID(userID).SetMobile(mobile).SetUpdatedBy(userID).SetUpdatedAt(time.Now()).Exec(ctx); err != nil {
		r.log.Errorf(ctx, "update mobile of user [%d] failed: %s", userID, err.Error())
		return false, authenticationV1.ErrorInternalServerError("update mobile failed")
	}

	if _, err = tx.UserCredential.Delete().
		Where(
			usercredential.UserIDEQ(userID),
			usercredential.IdentityTypeEQ(usercredential.IdentityTypePhone),
		).Exec(ctx); err != nil {
		r.log.Errorf(ctx, "delete phone credentials of user [%d] failed: %s", userID, err.Error())
		return false, authenticationV1.ErrorInternalServerError("delete phone credential failed")
	}
	if err = tx.UserCredential.Create().
		SetUserID(userID).
		SetTenantID(0).
		SetIdentityType(usercredential.IdentityTypePhone).
		SetIdentifier(mobile).
		SetCredentialType(usercredential.CredentialTypePasswordHash).
		SetCredential(phoneCredentialPlaceholder).
		SetIsPrimary(false).
		SetStatus(usercredential.StatusEnabled).
		SetCreatedAt(time.Now()).
		Exec(ctx); err != nil {
		r.log.Errorf(ctx, "insert phone credential of user [%d] failed: %s", userID, err.Error())
		return false, authenticationV1.ErrorInternalServerError("insert phone credential failed")
	}

	if commitErr := commitEntTx(tx, &txCommitted); commitErr != nil {
		r.log.Errorf(ctx, "transaction commit failed: %s", commitErr.Error())
		return false, authenticationV1.ErrorInternalServerError("transaction commit failed")
	}
	return true, nil
}
