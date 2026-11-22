package data

import (
	"context"
	"errors"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"go-wind-admin/third_party/go-crud/viewer"

	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/predicate"
	"go-wind-admin/app/admin/service/internal/data/ent/privacy"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/ent/usercredential"
)

// 微信小程序绑定（openid → 用户）存在 sys_user_credentials 里，一行一个绑定：
// identity_type=SOCIAL_OAUTH，provider=wechat_miniapp，provider_account_id=identifier=openid，
// credential_type=OAUTH_TOKEN，status=ENABLED，tenant_id=0。credential 字段不设置
// （schema 的 NotEmpty 不接受空串）。不记 UnionID，不写 extra_info。
//
// 「一个 openid 只对应一个用户」由唯一索引 (tenant_id, provider, provider_account_id) 保证。
// 绑定就是小程序的长期凭证：令牌过期后凭它静默续期，下线就是删掉它。

// MiniappProvider 绑定行的 provider 值。
const MiniappProvider = "wechat_miniapp"

var (
	// ErrMiniappBoundToOther 这个 openid 已经绑定了别的用户。不自动改挂。
	ErrMiniappBoundToOther = errors.New("miniapp openid is bound to another user")
	// ErrMiniappBindStale 绑定事务里重新确认没通过：手机号或密码在认证之后变了，不写绑定。
	ErrMiniappBindStale = errors.New("credential changed since authentication")
	// ErrMiniappUserDisabled 绑定事务里重新确认时用户已不是启用状态。
	ErrMiniappUserDisabled = errors.New("user is not enabled")
	// ErrMiniappBindingDisabled 这个微信在本用户上的绑定行被停用了，不能用来登录。
	ErrMiniappBindingDisabled = errors.New("miniapp binding is disabled")
	// ErrMiniappBindUnconfirmed 撞了 openid 的唯一索引，但重读时没有已提交的行：归属无法确认，可重试。
	ErrMiniappBindUnconfirmed = errors.New("miniapp binding ownership could not be confirmed")
	// ErrMiniappBindingGone 签发前在用户行锁下重新确认时，会话所用的绑定已经不在了（被解绑、下线或凭证变更删掉）。
	ErrMiniappBindingGone = errors.New("miniapp binding no longer exists")
	// ErrMiniappTargetNotFound 解绑的目标用户不存在，或不在操作人的数据范围内。
	ErrMiniappTargetNotFound = errors.New("target user not found")
)

// MiniappBinding 一条绑定。
type MiniappBinding struct {
	ID        uint32
	UserID    uint32
	OpenID    string
	CreatedAt *time.Time
}

// MiniappBindCheck 绑定事务里、锁住用户行之后要重新确认的认证依据。两个字段二选一。
type MiniappBindCheck struct {
	// Mobile 手机号路径：sys_users.mobile 仍等于微信返回的号码。
	Mobile *string
	// PasswordSnapshot 密码路径：密码凭证行和校验密码时读到的是同一个值
	// （FindUserCredentialWithSnapshot 返回的快照）。
	PasswordSnapshot *string
}

// miniappLoginContext 两个公开的微信登录接口没有登录身份，查询按登录的方式放行。
// 只用于本文件里条件写死的查询。
func miniappLoginContext(ctx context.Context) context.Context {
	ctx = viewer.WithContext(ctx, viewer.NewNoopContext())
	return privacy.DecisionContext(ctx, privacy.Allow)
}

// miniappBindingQuery 按已验证的 openid 查「能用来登录的」绑定，条件写死：只认 ENABLED 的行。
// 静默续期用它。
func miniappBindingQuery(client *ent.Client, openid string) *ent.UserCredentialQuery {
	return miniappBindingOwnerQuery(client, openid).Where(usercredential.StatusEQ(usercredential.StatusEnabled))
}

// miniappBindingOwnerQuery 按 openid 查这个微信归谁，不看状态。
// 被停用的绑定行仍然占着唯一索引：判断归属时必须看得到它，否则它属于别的账号时
// 只能撞索引，得不到明确的冲突。能不能登录是另一回事，由 miniappBindingQuery 管。
func miniappBindingOwnerQuery(client *ent.Client, openid string) *ent.UserCredentialQuery {
	return client.UserCredential.Query().Where(
		usercredential.TenantIDEQ(0),
		usercredential.IdentityTypeEQ(usercredential.IdentityTypeSocialOauth),
		usercredential.ProviderEQ(MiniappProvider),
		usercredential.ProviderAccountIDEQ(openid),
	)
}

// miniappBindingIndex 保证「一个 openid 只对应一个用户」的唯一索引。
const miniappBindingIndex = "idx_sys_user_cred_tenant_provider_account"

// isMiniappBindingConflict err 是不是撞了 openid 的唯一索引（别的事务刚插入了同一个 openid）。
// 其他约束错误不算：它们不是归属冲突，不能走「重读归属」那条路。
// PostgreSQL 和 MySQL 的报错带索引名，SQLite 的报错带列名。
func isMiniappBindingConflict(err error) bool {
	if err == nil || !ent.IsConstraintError(err) {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, miniappBindingIndex) || strings.Contains(msg, usercredential.FieldProviderAccountID)
}

func toMiniappBinding(e *ent.UserCredential) *MiniappBinding {
	b := &MiniappBinding{ID: e.ID, CreatedAt: e.CreatedAt}
	if e.UserID != nil {
		b.UserID = *e.UserID
	}
	if e.ProviderAccountID != nil {
		b.OpenID = *e.ProviderAccountID
	}
	return b
}

// FindMiniappBinding 按已验证的 openid 查绑定。没有绑定返回 (nil, nil)。
func (r *UserCredentialRepo) FindMiniappBinding(ctx context.Context, openid string) (*MiniappBinding, error) {
	entity, err := miniappBindingQuery(r.entClient.Client(), openid).Only(miniappLoginContext(ctx))
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		r.log.Errorf(ctx, "query miniapp binding failed: %s", err.Error())
		return nil, err
	}
	return toMiniappBinding(entity), nil
}

// BindMiniapp 认证通过后把 openid 绑到用户上。一个事务里：
//  1. 锁住该用户的 sys_users 行；
//  2. 重新确认用户是启用状态，以及 check 里的认证依据没变；
//  3. 查这个 openid 的绑定：属于别的用户返回 ErrMiniappBoundToOther，属于本用户不动，没有就插入；
//  4. 提交。
//
// 凭证变更的事务也先锁同一行，所以对同一个用户是串行的：绑定要么在凭证变更之前提交（随后被删掉），
// 要么在之后开始（第 2 步发现凭证已变，返回 ErrMiniappBindStale）。
// 并发首次绑定撞唯一索引时重新读一次：是本用户就按已存在处理，是别的用户就返回冲突。
func (r *UserCredentialRepo) BindMiniapp(ctx context.Context, userID uint32, openid string, check MiniappBindCheck) (bindingID uint32, err error) {
	ctx = miniappLoginContext(ctx)

	bindingID, conflict, err := r.bindMiniappTx(ctx, userID, openid, check)
	if !conflict {
		return bindingID, err
	}
	// 撞了 openid 的唯一索引。重读归属放在这里：bindMiniappTx 已经返回，失败的事务已经回滚，
	// 连接和用户行锁都放掉了。在事务里重读的话，连接池没有空闲连接时会等自己占着的那个连接。
	return r.resolveBindingAfterConflict(ctx, userID, openid)
}

// bindMiniappTx BindMiniapp 的事务部分。conflict 为 true 表示撞了 openid 的唯一索引，
// 事务已作废（函数返回时回滚），调用方在事务外重读归属。
func (r *UserCredentialRepo) bindMiniappTx(ctx context.Context, userID uint32, openid string, check MiniappBindCheck) (bindingID uint32, conflict bool, err error) {
	id, err := r.bindMiniappInTx(ctx, userID, openid, check)
	if isMiniappBindingConflict(err) {
		r.log.Warnf(ctx, "miniapp binding of user [%d] hit the openid unique index, re-reading ownership", userID)
		return 0, true, nil
	}
	return id, false, err
}

func (r *UserCredentialRepo) bindMiniappInTx(ctx context.Context, userID uint32, openid string, check MiniappBindCheck) (bindingID uint32, err error) {
	tx, err := r.entClient.Client().Tx(ctx)
	if err != nil {
		r.log.Errorf(ctx, "start transaction failed: %s", err.Error())
		return 0, err
	}
	var txCommitted bool
	defer finishEntTx(tx, &txCommitted, func(rollbackErr error) {
		r.log.Errorf(ctx, "transaction rollback failed: %s", rollbackErr.Error())
	})

	current, err := lockUserRow(ctx, tx, r.entClient.Driver().Dialect(), userID)
	if err != nil {
		r.log.Errorf(ctx, "lock user [%d] failed: %s", userID, err.Error())
		return 0, err
	}
	if current.Status == nil || *current.Status != user.StatusNormal {
		return 0, ErrMiniappUserDisabled
	}
	if check.Mobile != nil && derefString(current.Mobile) != *check.Mobile {
		return 0, ErrMiniappBindStale
	}
	if check.PasswordSnapshot != nil {
		snapshot, serr := passwordSnapshotInTx(ctx, tx, userID)
		if serr != nil {
			r.log.Errorf(ctx, "read password credential of user [%d] failed: %s", userID, serr.Error())
			return 0, serr
		}
		if snapshot != *check.PasswordSnapshot {
			return 0, ErrMiniappBindStale
		}
	}

	// 归属判断不看状态：被停用的绑定行属于别的账号时同样是冲突。
	existing, err := miniappBindingOwnerQuery(tx.Client(), openid).Only(ctx)
	switch {
	case err == nil:
		if existing.UserID == nil || *existing.UserID != userID {
			return 0, ErrMiniappBoundToOther
		}
		if existing.Status == nil || *existing.Status != usercredential.StatusEnabled {
			// 本用户的这条绑定被停用了：不能登录，也不替管理员重新启用。
			return 0, ErrMiniappBindingDisabled
		}
		// 已经绑在本用户上：不动。
		bindingID = existing.ID
	case ent.IsNotFound(err):
		created, cerr := tx.UserCredential.Create().
			SetUserID(userID).
			SetTenantID(0).
			SetIdentityType(usercredential.IdentityTypeSocialOauth).
			SetIdentifier(openid).
			SetCredentialType(usercredential.CredentialTypeOauthToken).
			SetIsPrimary(false).
			SetStatus(usercredential.StatusEnabled).
			SetProvider(MiniappProvider).
			SetProviderAccountID(openid).
			SetCreatedAt(time.Now()).
			Save(ctx)
		if cerr != nil {
			// 撞 openid 唯一索引的错误原样返回，由 bindMiniappTx 识别；回滚由 defer 完成。
			// 约束错误的文本只有索引名或列名，没有 openid 的值。
			if !isMiniappBindingConflict(cerr) {
				r.log.Errorf(ctx, "insert miniapp binding of user [%d] failed: %s", userID, cerr.Error())
			}
			return 0, cerr
		}
		bindingID = created.ID
	default:
		r.log.Errorf(ctx, "query miniapp binding failed: %s", err.Error())
		return 0, err
	}

	if commitErr := commitEntTx(tx, &txCommitted); commitErr != nil {
		if !isMiniappBindingConflict(commitErr) {
			r.log.Errorf(ctx, "transaction commit failed: %s", commitErr.Error())
		}
		return 0, commitErr
	}
	return bindingID, nil
}

// resolveBindingAfterConflict 插入撞了唯一索引之后重新读一次归属。
// 必须在失败的事务回滚之后调用：这里用的是另一个连接，读已提交的行。
func (r *UserCredentialRepo) resolveBindingAfterConflict(ctx context.Context, userID uint32, openid string) (uint32, error) {
	entity, err := miniappBindingOwnerQuery(r.entClient.Client(), openid).Only(miniappLoginContext(ctx))
	if err != nil {
		if ent.IsNotFound(err) {
			// 撞了索引却读不到：对方的事务又回滚了，或者行刚被删掉。归属无法确认，让调用方按可重试处理。
			r.log.Warnf(ctx, "miniapp binding of user [%d]: unique index conflict but no committed row found", userID)
			return 0, ErrMiniappBindUnconfirmed
		}
		r.log.Errorf(ctx, "re-read miniapp binding after conflict failed: %s", err.Error())
		return 0, err
	}
	if entity.UserID == nil || *entity.UserID != userID {
		return 0, ErrMiniappBoundToOther
	}
	if entity.Status == nil || *entity.Status != usercredential.StatusEnabled {
		return 0, ErrMiniappBindingDisabled
	}
	return entity.ID, nil
}

// MiniappIssueGuard 签发 app 令牌之前、在用户行锁下要重新确认的东西。
type MiniappIssueGuard struct {
	// BindingID 这次会话所用的绑定行。非 0 时确认它还在、属于这个用户、是启用的。
	BindingID uint32
	// PasswordSnapshot 密码登录时校验密码读到的凭证值。非空时确认密码凭证值没变。
	PasswordSnapshot *string
}

// IssueUnderUserLock 锁住用户行，重新确认用户启用、会话所用的绑定还在、密码没变，
// 然后在持有锁的情况下执行 issue（往 Redis 写令牌）。
//
// 为什么需要它：签发按撤销版本条件写入，但撤销版本靠 Redis 的 INCR 推进。
// 解绑、改密的数据库部分已经提交而 INCR 失败时，在途的登录或续期手里的旧版本仍然对得上，
// 会给一条已经删掉的绑定（或已经改掉的密码）签出一个全新的令牌。
// 删绑定和改凭证的事务都先锁同一行，所以这里要么在它们之前写完（随后被它们的吊销删掉），
// 要么在它们提交之后才拿到锁（确认不通过，不写）。
func (r *UserCredentialRepo) IssueUnderUserLock(ctx context.Context, userID uint32, guard MiniappIssueGuard, issue func() error) error {
	ctx = miniappLoginContext(ctx)

	tx, err := r.entClient.Client().Tx(ctx)
	if err != nil {
		r.log.Errorf(ctx, "start transaction failed: %s", err.Error())
		return err
	}
	// 只读事务：用完回滚即可。
	var txCommitted bool
	defer finishEntTx(tx, &txCommitted, func(rollbackErr error) {
		r.log.Errorf(ctx, "transaction rollback failed: %s", rollbackErr.Error())
	})

	current, err := lockUserRow(ctx, tx, r.entClient.Driver().Dialect(), userID)
	if err != nil {
		r.log.Errorf(ctx, "lock user [%d] failed: %s", userID, err.Error())
		return err
	}
	if current.Status == nil || *current.Status != user.StatusNormal {
		return ErrMiniappUserDisabled
	}
	if guard.BindingID != 0 {
		exists, qerr := tx.UserCredential.Query().
			Where(
				usercredential.IDEQ(guard.BindingID),
				usercredential.UserIDEQ(userID),
				usercredential.TenantIDEQ(0),
				usercredential.ProviderEQ(MiniappProvider),
				usercredential.StatusEQ(usercredential.StatusEnabled),
			).
			Exist(ctx)
		if qerr != nil {
			r.log.Errorf(ctx, "re-check miniapp binding [%d] failed: %s", guard.BindingID, qerr.Error())
			return qerr
		}
		if !exists {
			return ErrMiniappBindingGone
		}
	}
	if guard.PasswordSnapshot != nil {
		snapshot, serr := passwordSnapshotInTx(ctx, tx, userID)
		if serr != nil {
			r.log.Errorf(ctx, "read password credential of user [%d] failed: %s", userID, serr.Error())
			return serr
		}
		if snapshot != *guard.PasswordSnapshot {
			return ErrMiniappBindStale
		}
	}
	return issue()
}

// ListMiniappBindings 列出用户的微信绑定，按绑定时间先后。调用方负责先确认操作人看得到这个用户。
func (r *UserCredentialRepo) ListMiniappBindings(ctx context.Context, userID uint32) ([]*MiniappBinding, error) {
	entities, err := r.entClient.Client().UserCredential.Query().
		Where(
			usercredential.TenantIDEQ(0),
			usercredential.UserIDEQ(userID),
			usercredential.IdentityTypeEQ(usercredential.IdentityTypeSocialOauth),
			usercredential.ProviderEQ(MiniappProvider),
		).
		Order(ent.Asc(usercredential.FieldID)).
		All(viewer.WithSystemContext(ctx))
	if err != nil {
		r.log.Errorf(ctx, "list miniapp bindings of user [%d] failed: %s", userID, err.Error())
		return nil, err
	}
	bindings := make([]*MiniappBinding, 0, len(entities))
	for _, e := range entities {
		bindings = append(bindings, toMiniappBinding(e))
	}
	return bindings, nil
}

// MiniappUnbindGuard 操作人解除别人的绑定时，在事务里、锁住目标用户行之后要复核的东西。
// 零值表示不复核（用户下线自己的会话）。
type MiniappUnbindGuard struct {
	// EnforceDataScope 为 true 时按 ctx 里操作人的数据范围锁目标用户行，范围外当作不存在。
	EnforceDataScope bool
	// Check 非空时用锁内读到的目标用户当前角色调用它（权限上限检查）。返回错误则什么都不删。
	Check func(ctx context.Context, roleIDs []uint32) error
}

// DeleteMiniappBinding 删除一条绑定。一个事务里：锁目标用户行，按 guard 复核，再删除。
// 删除条件同时限定用户、绑定 ID、provider 和租户：拿别的用户的绑定 ID 来删，影响 0 行，
// 没有任何绑定被删。删除 0 行也算成功（幂等）。
//
// 复核放在锁里：授权读到的数据范围和角色与删除在同一把锁下，中间不会被并发的角色提升或
// 调动插进来（改用户角色的入口也先锁这一行）。先锁用户行也让它和签发、绑定、凭证变更串行。
// 目标用户不存在或在数据范围外返回 ErrMiniappTargetNotFound。
func (r *UserCredentialRepo) DeleteMiniappBinding(ctx context.Context, userID, bindingID uint32, guard MiniappUnbindGuard) error {
	// 数据范围的谓词按操作人的 ctx 算，查询本身用系统视图执行。
	var scopeSel func(*sql.Selector)
	if guard.EnforceDataScope {
		var err error
		if scopeSel, err = userScopeSelector(WithDataScope(ctx)); err != nil {
			r.log.Errorf(ctx, "resolve data scope failed: %v", err)
			return err
		}
	}
	sysCtx := viewer.WithSystemContext(ctx)

	tx, err := r.entClient.Client().Tx(sysCtx)
	if err != nil {
		r.log.Errorf(ctx, "start transaction failed: %s", err.Error())
		return err
	}
	var txCommitted bool
	defer finishEntTx(tx, &txCommitted, func(rollbackErr error) {
		r.log.Errorf(ctx, "transaction rollback failed: %s", rollbackErr.Error())
	})

	lock := tx.User.Query().Where(user.IDEQ(userID))
	if scopeSel != nil {
		lock = lock.Where(predicate.User(scopeSel))
	}
	if r.entClient.Driver().Dialect() != dialect.SQLite {
		lock = lock.ForUpdate()
	}
	if _, err = lock.Only(sysCtx); err != nil {
		if ent.IsNotFound(err) {
			return ErrMiniappTargetNotFound
		}
		r.log.Errorf(ctx, "lock user [%d] failed: %s", userID, err.Error())
		return err
	}

	if guard.Check != nil {
		roleIDs, rerr := userRoleIDsInTx(ctx, tx, userID)
		if rerr != nil {
			r.log.Errorf(ctx, "list roles of user [%d] in tx failed: %s", userID, rerr.Error())
			return rerr
		}
		if err = guard.Check(ctx, roleIDs); err != nil {
			return err
		}
	}

	if _, err = tx.UserCredential.Delete().
		Where(
			usercredential.IDEQ(bindingID),
			usercredential.UserIDEQ(userID),
			usercredential.TenantIDEQ(0),
			usercredential.ProviderEQ(MiniappProvider),
		).
		Exec(sysCtx); err != nil {
		r.log.Errorf(ctx, "delete miniapp binding [%d] of user [%d] failed: %s", bindingID, userID, err.Error())
		return err
	}
	if commitErr := commitEntTx(tx, &txCommitted); commitErr != nil {
		r.log.Errorf(ctx, "transaction commit failed: %s", commitErr.Error())
		return commitErr
	}
	return nil
}

// deleteMiniappBindingsInTx 在凭证变更的事务里删掉该用户的全部微信绑定。调用前要先锁住用户行。
// 改密、重置密码、改手机号、清手机号都调它：旧凭证建立的绑定不能再用来续期。
// 和凭证变更同一个事务：要么都生效，要么都不生效。
func deleteMiniappBindingsInTx(ctx context.Context, tx *ent.Tx, userID uint32) error {
	_, err := tx.UserCredential.Delete().
		Where(
			usercredential.UserIDEQ(userID),
			usercredential.TenantIDEQ(0),
			usercredential.ProviderEQ(MiniappProvider),
		).
		Exec(viewer.WithSystemContext(ctx))
	return err
}

// passwordSnapshotInTx 在事务里读用户的密码凭证值。调用前要先锁住用户行。
func passwordSnapshotInTx(ctx context.Context, tx *ent.Tx, userID uint32) (string, error) {
	entity, err := tx.UserCredential.Query().
		Select(usercredential.FieldCredential).
		Where(
			usercredential.TenantIDEQ(0),
			usercredential.UserIDEQ(userID),
			usercredential.IdentityTypeEQ(usercredential.IdentityTypeUsername),
		).
		Only(ctx)
	if err != nil {
		return "", err
	}
	return derefString(entity.Credential), nil
}
