package data

import (
	"context"
	"errors"
	"time"

	"go-wind-admin/third_party/go-crud/viewer"

	"go-wind-admin/app/admin/service/internal/data/ent"
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

// miniappBindingQuery 按已验证的 openid 查绑定，条件写死。
func miniappBindingQuery(client *ent.Client, openid string) *ent.UserCredentialQuery {
	return client.UserCredential.Query().Where(
		usercredential.TenantIDEQ(0),
		usercredential.IdentityTypeEQ(usercredential.IdentityTypeSocialOauth),
		usercredential.ProviderEQ(MiniappProvider),
		usercredential.ProviderAccountIDEQ(openid),
		usercredential.StatusEQ(usercredential.StatusEnabled),
	)
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

	existing, err := miniappBindingQuery(tx.Client(), openid).Only(ctx)
	switch {
	case err == nil:
		if existing.UserID == nil || *existing.UserID != userID {
			return 0, ErrMiniappBoundToOther
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
			if ent.IsConstraintError(cerr) {
				// 别的事务刚刚插入了同一个 openid。本事务在 PostgreSQL 上已经作废，回滚后再读一次。
				return r.resolveBindingAfterConflict(ctx, userID, openid)
			}
			r.log.Errorf(ctx, "insert miniapp binding of user [%d] failed: %s", userID, cerr.Error())
			return 0, cerr
		}
		bindingID = created.ID
	default:
		r.log.Errorf(ctx, "query miniapp binding failed: %s", err.Error())
		return 0, err
	}

	if commitErr := commitEntTx(tx, &txCommitted); commitErr != nil {
		if ent.IsConstraintError(commitErr) {
			return r.resolveBindingAfterConflict(ctx, userID, openid)
		}
		r.log.Errorf(ctx, "transaction commit failed: %s", commitErr.Error())
		return 0, commitErr
	}
	return bindingID, nil
}

// resolveBindingAfterConflict 插入撞了唯一索引之后重新读一次（事务外，读已提交的行）。
func (r *UserCredentialRepo) resolveBindingAfterConflict(ctx context.Context, userID uint32, openid string) (uint32, error) {
	binding, err := r.FindMiniappBinding(ctx, openid)
	if err != nil {
		return 0, err
	}
	if binding == nil {
		// 撞了索引却读不到：对方还没提交或者又被删了。归属无法确认，让调用方按可重试处理。
		return 0, errors.New("miniapp binding ownership could not be confirmed")
	}
	if binding.UserID != userID {
		return 0, ErrMiniappBoundToOther
	}
	return binding.ID, nil
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

// DeleteMiniappBinding 删除一条绑定。条件同时限定用户、绑定 ID、provider 和租户：
// 拿别的用户的绑定 ID 来删，影响 0 行，没有任何绑定被删。删除 0 行也算成功（幂等）。
func (r *UserCredentialRepo) DeleteMiniappBinding(ctx context.Context, userID, bindingID uint32) error {
	_, err := r.entClient.Client().UserCredential.Delete().
		Where(
			usercredential.IDEQ(bindingID),
			usercredential.UserIDEQ(userID),
			usercredential.TenantIDEQ(0),
			usercredential.ProviderEQ(MiniappProvider),
		).
		Exec(viewer.WithSystemContext(ctx))
	if err != nil {
		r.log.Errorf(ctx, "delete miniapp binding [%d] of user [%d] failed: %s", bindingID, userID, err.Error())
		return err
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
