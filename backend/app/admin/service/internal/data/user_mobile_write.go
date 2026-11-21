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
	"go-wind-admin/app/admin/service/internal/data/ent/userrole"

	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"

	"go-wind-admin/pkg/phone"
)

// 手机号是登录凭证（小程序一键登录按 sys_users.mobile 精确匹配），写它按写凭证来管。
// 本文件是凭证写入路径在仓储事务里的公共部分：管理员更新用户（userRepo.UpdateGuarded）、
// 管理员重置密码（UserCredentialRepo.ResetCredentialGuarded）和自助短信改号（BindVerifiedPhone）。
// 它们都先锁目标用户的 sys_users 行，对同一个用户串行。

// UserUpdateGuard 管理员更新用户时对登录凭证（手机号、密码）的把关。
type UserUpdateGuard struct {
	// CheckCredentialWrite 这次更新要写凭证（号码确实要变，含清空；或附带重置密码）时调用。
	// 在更新事务里、锁住用户行之后、任何写入之前调用；roleIDs 是锁内读到的目标用户当前角色，
	// 也就是这次请求改写角色之前的角色。返回错误则整个更新回滚。
	CheckCredentialWrite func(ctx context.Context, tx *ent.Tx, userID uint32, roleIDs []uint32) error
	// ResetPassword 非空表示这次请求附带重置密码。用户行和关联写完之后、在同一个事务里调用。
	ResetPassword func(ctx context.Context, tx *ent.Tx) error
}

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

// userRoleIDsInTx 在事务里读目标用户当前的角色。调用前要先锁住用户行：
// 改用户角色的入口（UpdateGuarded）也先锁这一行，所以锁内读到的角色不会被并发改掉。
func userRoleIDsInTx(ctx context.Context, tx *ent.Tx, userID uint32) ([]uint32, error) {
	rows, err := tx.UserRole.Query().
		Where(userrole.UserIDEQ(userID)).
		Select(userrole.FieldRoleID).
		All(viewer.WithSystemContext(ctx))
	if err != nil {
		return nil, err
	}
	ids := make([]uint32, 0, len(rows))
	for _, row := range rows {
		if row.RoleID != nil {
			ids = append(ids, *row.RoleID)
		}
	}
	return ids, nil
}

// claimMobile 认领一个手机号：检查租户 0 里没有别的用户在用，并让不同用户并发改到同一个号码的事务串行。
// 各事务只锁了自己的用户行，sys_users.mobile 上只有普通索引，不串行的话两个事务都看不到对方、都能提交。
//   - PostgreSQL：按规范号码取事务级 advisory lock，后到的事务等前一个结束后再查占用。
//   - MySQL 等其他带行锁的数据库：占用查询加 FOR UPDATE。号码没人用时两个事务各拿到索引间隙锁，
//     随后的写入互相等待，数据库判死锁回滚其中一个，最多一方提交。
//   - SQLite：写事务本身串行。
//
// used 为 true 表示号码已被别的用户使用。用系统视图查，不受操作人数据范围影响。
func claimMobile(ctx context.Context, tx *ent.Tx, dialectName string, userID uint32, mobile string) (used bool, err error) {
	if isPostgresDriver(dialectName) {
		if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "sys_users.mobile:"+mobile); err != nil {
			return false, err
		}
	}
	q := tx.User.Query().
		Where(
			user.MobileEQ(mobile),
			user.IDNEQ(userID),
			user.Or(user.TenantIDEQ(0), user.TenantIDIsNil()),
		)
	if !isPostgresDriver(dialectName) && dialectName != dialect.SQLite {
		q = q.ForUpdate()
	}
	return q.Exist(viewer.WithSystemContext(ctx))
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// resolveMobileWrite 算出这次更新对 mobile 的实际效果，在字段屏蔽、mask 过滤、掩码回传都处理完之后调用。
// 规则和 UpdateOne 的落库行为一致：调用方没给 mask 时写 Data 里非空指针的字段；
// 有 mask 时只写路径里的字段，值为空指针表示清空。
func resolveMobileWrite(req *identityV1.UpdateUserRequest, explicitMask bool) (writes bool, value string) {
	if explicitMask {
		writes = slices.Contains(req.GetUpdateMask().GetPaths(), "mobile")
	} else {
		writes = req.GetData().Mobile != nil
	}
	if !writes {
		return false, ""
	}
	return true, strings.TrimSpace(req.GetData().GetMobile())
}

// guardCredentialWrite 管理员更新用户的事务里、写入之前调用，current 是已经锁住的用户行。
// 算出号码是否确实要变；要写凭证（号码变更或附带重置密码）时，用锁内读到的角色过权限检查；
// 号码变更且非空时认领号码。写入值会换成规范后的号码。
func (r *userRepo) guardCredentialWrite(ctx context.Context, tx *ent.Tx, current *ent.User,
	req *identityV1.UpdateUserRequest, explicitMask bool, guard *UserUpdateGuard) (UserUpdateOutcome, error) {
	var outcome UserUpdateOutcome

	writes, newMobile := resolveMobileWrite(req, explicitMask)
	if writes {
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
		outcome.MobileChanged = newMobile != currentMobile
	}

	resetsPassword := guard != nil && guard.ResetPassword != nil
	if (outcome.MobileChanged || resetsPassword) && guard != nil && guard.CheckCredentialWrite != nil {
		roleIDs, err := userRoleIDsInTx(ctx, tx, req.GetId())
		if err != nil {
			r.log.Errorf(ctx, "list roles of user [%d] in tx failed: %s", req.GetId(), err.Error())
			return outcome, identityV1.ErrorInternalServerError("query user roles failed")
		}
		if err = guard.CheckCredentialWrite(ctx, tx, req.GetId(), roleIDs); err != nil {
			return outcome, err
		}
	}

	if outcome.MobileChanged && newMobile != "" {
		used, err := claimMobile(ctx, tx, r.entClient.Driver().Dialect(), req.GetId(), newMobile)
		if err != nil {
			r.log.Errorf(ctx, "check mobile in use failed: %s", err.Error())
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

	used, err := claimMobile(ctx, tx, r.entClient.Driver().Dialect(), userID, mobile)
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

// ResetCredentialGuarded 管理员重置密码：一个事务里锁用户行、读当前角色、过 check、再改密码。
// 授权读取和密码写入在同一把锁下，中间不会被并发的角色变更插进来。check 返回错误则什么都不写。
func (r *UserCredentialRepo) ResetCredentialGuarded(ctx context.Context, userID uint32, req *authenticationV1.ResetCredentialRequest,
	check func(ctx context.Context, tx *ent.Tx, roleIDs []uint32) error) error {
	tx, err := r.entClient.Client().Tx(ctx)
	if err != nil {
		r.log.Errorf(ctx, "start transaction failed: %s", err.Error())
		return authenticationV1.ErrorInternalServerError("start transaction failed")
	}
	var txCommitted bool
	defer finishEntTx(tx, &txCommitted, func(rollbackErr error) {
		r.log.Errorf(ctx, "transaction rollback failed: %s", rollbackErr.Error())
	})

	if _, err = lockUserRow(viewer.WithSystemContext(ctx), tx, r.entClient.Driver().Dialect(), userID); err != nil {
		if ent.IsNotFound(err) {
			return authenticationV1.ErrorNotFound("user not found")
		}
		r.log.Errorf(ctx, "lock user [%d] failed: %s", userID, err.Error())
		return authenticationV1.ErrorInternalServerError("query user failed")
	}
	if check != nil {
		roleIDs, rerr := userRoleIDsInTx(ctx, tx, userID)
		if rerr != nil {
			r.log.Errorf(ctx, "list roles of user [%d] in tx failed: %s", userID, rerr.Error())
			return authenticationV1.ErrorInternalServerError("query user roles failed")
		}
		if err = check(ctx, tx, roleIDs); err != nil {
			return err
		}
	}
	if err = r.ResetCredentialWithTx(ctx, tx, userID, req); err != nil {
		return err
	}
	if commitErr := commitEntTx(tx, &txCommitted); commitErr != nil {
		r.log.Errorf(ctx, "transaction commit failed: %s", commitErr.Error())
		return authenticationV1.ErrorInternalServerError("transaction commit failed")
	}
	return nil
}
