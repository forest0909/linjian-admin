package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"

	paginationV1 "go-wind-admin/third_party/go-crud/api/gen/go/pagination/v1"
	"go-wind-admin/third_party/go-utils/aggregator"
	"go-wind-admin/third_party/go-utils/crypto"
	"go-wind-admin/third_party/go-utils/sliceutil"
	"go-wind-admin/third_party/go-utils/trans"
	"go-wind-admin/third_party/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/third_party/kratos-bootstrap/logger"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"

	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
	permissionV1 "go-wind-admin/api/gen/go/permission/service/v1"

	"go-wind-admin/pkg/constants"
	"go-wind-admin/pkg/middleware/auth"
	"go-wind-admin/third_party/go-crud/viewer"
)

type UserService struct {
	adminV1.UserServiceHTTPServer

	log *bLogger.Helper

	userRepo           data.UserRepo
	userCredentialRepo *data.UserCredentialRepo

	roleRepo     *data.RoleRepo
	positionRepo *data.PositionRepo
	orgUnitRepo  *data.OrgUnitRepo

	membershipRepo *data.MembershipRepo

	authenticator *data.Authenticator
}

func NewUserService(
	ctx *bootstrap.Context,
	userRepo data.UserRepo,
	roleRepo *data.RoleRepo,
	userCredentialRepo *data.UserCredentialRepo,
	positionRepo *data.PositionRepo,
	orgUnitRepo *data.OrgUnitRepo,
	membershipRepo *data.MembershipRepo,
	authenticator *data.Authenticator,
) *UserService {
	svc := &UserService{
		log:                ctx.NewLoggerHelper("user/service/admin-service"),
		userRepo:           userRepo,
		roleRepo:           roleRepo,
		userCredentialRepo: userCredentialRepo,
		positionRepo:       positionRepo,
		orgUnitRepo:        orgUnitRepo,
		membershipRepo:     membershipRepo,
		authenticator:      authenticator,
	}

	svc.init()

	return svc
}

func (s *UserService) init() {
	ctx := viewer.WithSystemContext(context.Background())

	if count, _ := s.userRepo.Count(ctx, nil); count == 0 {
		if err := s.createDefaultUser(ctx); err != nil {
			s.log.Errorf(ctx, "init default user data failed: %v", err)
		}
		return
	}

	// 自愈历史缺陷：空库部署时默认凭证曾因 admin 不满足等保口令复杂度被拒
	// （issue #58），初始化半途而废——用户行已建、凭证行缺失且错误被吞，
	// admin 从此无法登录。正常路径下每个用户创建时必带凭证，凭证表为空
	// 只可能是初始化半途失败所致；彼时 admin 是空表首行，id 必为 1，按种子补种。
	if count, _ := s.userCredentialRepo.Count(ctx, nil); count == 0 {
		if err := s.createDefaultUserCredentials(ctx, 0); err != nil {
			s.log.Errorf(ctx, "reseed default user credentials failed: %v", err)
		}
	}
}

func (s *UserService) extractRelationIDs(
	users []*identityV1.User,
	roleSet aggregator.ResourceMap[uint32, *permissionV1.Role],
	orgUnitSet aggregator.ResourceMap[uint32, *identityV1.OrgUnit],
	posSet aggregator.ResourceMap[uint32, *identityV1.Position],
) {
	for _, v := range users {
		if v == nil {
			continue
		}

		if id := v.GetRoleId(); id > 0 {
			roleSet[id] = nil
		}
		for _, roleId := range v.RoleIds {
			if roleId > 0 {
				roleSet[roleId] = nil
			}
		}

		if v.GetOrgUnitId() > 0 {
			orgUnitSet[v.GetOrgUnitId()] = nil
		}
		if len(v.OrgUnitIds) > 0 {
			for _, orgID := range v.OrgUnitIds {
				if orgID > 0 {
					orgUnitSet[orgID] = nil
				}
			}
		}

		if v.GetPositionId() > 0 {
			posSet[v.GetPositionId()] = nil
		}
		if len(v.PositionIds) > 0 {
			for _, posID := range v.PositionIds {
				if posID > 0 {
					posSet[posID] = nil
				}
			}
		}

	}
}

func (s *UserService) fetchRelationInfo(
	ctx context.Context,
	roleSet aggregator.ResourceMap[uint32, *permissionV1.Role],
	orgUnitSet aggregator.ResourceMap[uint32, *identityV1.OrgUnit],
	posSet aggregator.ResourceMap[uint32, *identityV1.Position],
) error {
	if len(roleSet) > 0 {
		roleIds := make([]uint32, 0, len(roleSet))
		for id := range roleSet {
			roleIds = append(roleIds, id)
		}

		roles, err := s.roleRepo.ListRolesByRoleIds(ctx, roleIds)
		if err != nil {
			s.log.Errorf(context.Background(), "query roles err: %v", err)
			return err
		}

		for _, role := range roles {
			roleSet[role.GetId()] = role
		}
	}

	if len(orgUnitSet) > 0 {
		orgUnitIds := make([]uint32, 0, len(orgUnitSet))
		for id := range orgUnitSet {
			orgUnitIds = append(orgUnitIds, id)
		}

		orgUnits, err := s.orgUnitRepo.ListOrgUnitsByIds(ctx, orgUnitIds)
		if err != nil {
			s.log.Errorf(context.Background(), "query orgUnits err: %v", err)
			return err
		}

		for _, orgUnit := range orgUnits {
			orgUnitSet[orgUnit.GetId()] = orgUnit
		}
	}

	if len(posSet) > 0 {
		posIds := make([]uint32, 0, len(posSet))
		for id := range posSet {
			posIds = append(posIds, id)
		}

		positions, err := s.positionRepo.ListPositionByIds(ctx, posIds)
		if err != nil {
			s.log.Errorf(context.Background(), "query positions err: %v", err)
			return err
		}

		for _, position := range positions {
			posSet[position.GetId()] = position
		}
	}

	return nil
}

func (s *UserService) bindRelations(
	users []*identityV1.User,
	roleSet aggregator.ResourceMap[uint32, *permissionV1.Role],
	orgUnitSet aggregator.ResourceMap[uint32, *identityV1.OrgUnit],
	posSet aggregator.ResourceMap[uint32, *identityV1.Position],
) {
	aggregator.PopulateMulti(
		users,
		roleSet,
		func(ou *identityV1.User) []uint32 { return ou.GetRoleIds() },
		func(ou *identityV1.User, r []*permissionV1.Role) {
			for _, role := range r {
				ou.RoleNames = append(ou.RoleNames, role.GetName())
				ou.Roles = append(ou.Roles, role.GetCode())
			}
		},
	)
	aggregator.Populate(
		users,
		roleSet,
		func(ou *identityV1.User) uint32 { return ou.GetRoleId() },
		func(ou *identityV1.User, r *permissionV1.Role) {
			ou.RoleNames = append(ou.RoleNames, r.GetName())
			ou.Roles = append(ou.Roles, r.GetCode())
		},
	)

	aggregator.PopulateMulti(
		users,
		posSet,
		func(ou *identityV1.User) []uint32 { return ou.GetPositionIds() },
		func(ou *identityV1.User, r []*identityV1.Position) {
			for _, pos := range r {
				ou.PositionNames = append(ou.PositionNames, pos.GetName())
			}
		},
	)
	aggregator.Populate(
		users,
		posSet,
		func(ou *identityV1.User) uint32 { return ou.GetPositionId() },
		func(ou *identityV1.User, r *identityV1.Position) {
			ou.PositionName = r.Name
		},
	)

	aggregator.PopulateMulti(
		users,
		orgUnitSet,
		func(ou *identityV1.User) []uint32 { return ou.GetOrgUnitIds() },
		func(ou *identityV1.User, orgs []*identityV1.OrgUnit) {
			for _, org := range orgs {
				ou.OrgUnitNames = append(ou.OrgUnitNames, org.GetName())
			}
		},
	)
	aggregator.Populate(
		users,
		orgUnitSet,
		func(ou *identityV1.User) uint32 { return ou.GetOrgUnitId() },
		func(ou *identityV1.User, org *identityV1.OrgUnit) {
			ou.OrgUnitName = org.Name
		},
	)
}

func (s *UserService) enrichRelations(ctx context.Context, users []*identityV1.User) error {
	var roleSet = make(aggregator.ResourceMap[uint32, *permissionV1.Role])
	var orgUnitSet = make(aggregator.ResourceMap[uint32, *identityV1.OrgUnit])
	var posSet = make(aggregator.ResourceMap[uint32, *identityV1.Position])

	s.extractRelationIDs(users, roleSet, orgUnitSet, posSet)
	if err := s.fetchRelationInfo(ctx, roleSet, orgUnitSet, posSet); err != nil {
		return err
	}
	s.bindRelations(users, roleSet, orgUnitSet, posSet)
	return nil
}

func (s *UserService) List(ctx context.Context, req *paginationV1.PagingRequest) (*identityV1.ListUserResponse, error) {
	resp, err := s.userRepo.List(data.WithDataScope(ctx), req)
	if err != nil {
		return nil, err
	}

	// 回填角色/组织/岗位名称是展示增强，失败不阻断列表，但要留日志
	if err = s.enrichRelations(ctx, resp.Items); err != nil {
		s.log.Errorf(ctx, "enrich user relations failed: %v", err)
	}

	return resp, nil
}

func (s *UserService) Count(ctx context.Context, req *paginationV1.PagingRequest) (*identityV1.CountUserResponse, error) {
	count, err := s.userRepo.Count(data.WithDataScope(ctx), req)
	if err != nil {
		return nil, err
	}

	return &identityV1.CountUserResponse{
		Count: uint64(count),
	}, nil
}

func (s *UserService) Get(ctx context.Context, req *identityV1.GetUserRequest) (*identityV1.User, error) {
	resp, err := s.userRepo.Get(data.WithDataScope(ctx), req)
	if err != nil {
		return nil, err
	}

	fakeItems := []*identityV1.User{resp}
	if err = s.enrichRelations(ctx, fakeItems); err != nil {
		s.log.Errorf(ctx, "enrich user relations failed: %v", err)
	}

	return resp, nil
}

func (s *UserService) Create(ctx context.Context, req *identityV1.CreateUserRequest) (resp *emptypb.Empty, err error) {
	if req.Data == nil {
		return nil, adminV1.ErrorBadRequest("invalid parameter")
	}

	// 获取操作人信息
	operator, err := auth.FromContext(ctx)
	if err != nil {
		return nil, err
	}

	// 获取操作者的用户信息
	_, err = s.userRepo.Get(ctx, &identityV1.GetUserRequest{
		QueryBy: &identityV1.GetUserRequest_Id{
			Id: operator.UserId,
		},
	})
	if err != nil {
		return nil, err
	}

	req.Data.CreatedBy = trans.Ptr(operator.UserId)
	// 单租户：用户固定落在租户 0，不接受调用方指定租户
	req.Data.TenantId = nil

	var roleIds []uint32
	if len(req.Data.GetRoleIds()) > 0 {
		roleIds = req.Data.GetRoleIds()
	}
	if req.Data.RoleId != nil && *req.Data.RoleId > 0 {
		roleIds = append(roleIds, *req.Data.RoleId)
	}
	roleIds = sliceutil.Unique(roleIds)
	if len(roleIds) == 0 {
		s.log.Errorf(ctx, "role_ids is required")
		return nil, adminV1.ErrorBadRequest("role_ids is required")
	}

	queryString := fmt.Sprintf(`{"id__in": "[%s]", "type": "SYSTEM"}`,
		sliceutil.NumberSliceToString(roleIds),
	)
	roles, err := s.roleRepo.List(ctx, &paginationV1.PagingRequest{
		NoPaging: trans.Ptr(true),
		FilteringType: &paginationV1.PagingRequest_Query{
			Query: queryString,
		},
	})
	if err != nil {
		s.log.Errorf(ctx, "query roles err: %v", err)
		return nil, err
	}

	if len(roles.Items) != len(roleIds) {
		s.log.Errorf(ctx, "some roles not found, requested role ids: %v", roleIds)
		return nil, adminV1.ErrorBadRequest("some roles not found")
	}
	if len(roles.Items) == 0 {
		s.log.Errorf(ctx, "at least one role is required")
		return nil, adminV1.ErrorBadRequest("at least one role is required")
	}

	req.Data.RoleId = nil
	req.Data.RoleIds = roleIds

	// 授权上限：非超级管理员只能分配权限点不超出自己的角色。
	if err = ensureRolesWithinOperator(ctx, s.roleRepo, s.userRepo.ListRoleIDsByUserID, operator, roleIds); err != nil {
		s.log.Errorf(ctx, "operator [%d] assign roles %v rejected: %v", operator.GetUserId(), roleIds, err)
		return nil, err
	}

	// 创建请求里的密码与改密、重置同一条传输契约：前端 AES 密文。
	// 必须显式给密码：不再回落到公开的统一默认密码。
	if len(req.GetPassword()) == 0 {
		s.log.Errorf(ctx, "create user without password rejected")
		return nil, adminV1.ErrorBadRequest("password is required")
	}
	plainPassword, derr := decryptTransportPassword(req.GetPassword())
	if derr != nil {
		s.log.Errorf(ctx, "decrypt create user password failed: %s", derr.Error())
		return nil, derr
	}
	req.Password = trans.Ptr(plainPassword)

	// 用户行、角色/组织/岗位关联、凭据在同一个事务里。
	// 凭据复杂度校验失败或写入过程 panic 时整笔回滚，避免留下没有密码的用户。
	var tx *ent.Tx
	tx, err = s.userRepo.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	err = data.RunInTx(tx, func() error {
		var user *identityV1.User
		if user, err = s.userRepo.CreateWithTx(ctx, tx, req.GetData()); err != nil {
			return err
		}

		// 测试注入：模拟凭据写入过程中 panic。生产路径保持 nil。
		if userCreateCredentialHook != nil {
			userCreateCredentialHook()
		}
		if err = s.userCredentialRepo.CreateWithTx(ctx, tx, &authenticationV1.UserCredential{
			UserId:   user.Id,
			TenantId: user.TenantId,

			IdentityType: authenticationV1.UserCredential_USERNAME.Enum(),
			Identifier:   req.Data.Username,

			CredentialType: authenticationV1.UserCredential_PASSWORD_HASH.Enum(),
			Credential:     req.Password,

			IsPrimary: trans.Ptr(true),
			Status:    authenticationV1.UserCredential_ENABLED.Enum(),
		}); err != nil {
			return err
		}
		return nil
	}, func(rollbackErr error) {
		s.log.Errorf(ctx, "transaction rollback failed: %s", rollbackErr.Error())
	})
	if err != nil {
		if errors.Is(err, data.ErrTxCommit) {
			s.log.Errorf(ctx, "transaction commit failed: %s", err.Error())
			return nil, identityV1.ErrorInternalServerError("transaction commit failed")
		}
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// userCreateCredentialHook 只给测试注入。生产保持 nil。
// 在凭据写入前调用，用来模拟写入过程中的 panic。
var userCreateCredentialHook func()

// decryptTransportPassword 解开前端 encryptPassword 的 base64(AES) 密文。
// 与 ResetCredential / ChangeCredential 的 NeedDecrypt 使用同一密钥和同一失败文案。
// 日志只记错误，不记密文。
func decryptTransportPassword(cipherText string) (string, error) {
	bytesPass, err := base64.StdEncoding.DecodeString(cipherText)
	if err != nil {
		return "", authenticationV1.ErrorBadRequest("invalid new credential format")
	}
	plainPassword, err := crypto.AesDecrypt(bytesPass, crypto.DefaultAESKey, nil)
	if err != nil {
		return "", authenticationV1.ErrorBadRequest("decrypt new credential failed")
	}
	return string(plainPassword), nil
}

func (s *UserService) Update(ctx context.Context, req *identityV1.UpdateUserRequest) (*emptypb.Empty, error) {
	if req.Data == nil {
		return nil, adminV1.ErrorBadRequest("invalid parameter")
	}

	// 获取操作人信息
	operator, err := auth.FromContext(ctx)
	if err != nil {
		return nil, err
	}

	// 获取操作者的用户信息
	_, err = s.userRepo.Get(ctx, &identityV1.GetUserRequest{
		QueryBy: &identityV1.GetUserRequest_Id{
			Id: operator.UserId,
		},
	})
	if err != nil {
		return nil, err
	}

	// 数据范围外的用户按不存在处理（404）。allow_missing 且目标确实不存在时照旧进入仓储创建。
	if err = s.requireUserInScopeForUpdate(ctx, req); err != nil {
		return nil, err
	}

	req.Data.UpdatedBy = trans.Ptr(operator.UserId)
	if req.UpdateMask != nil {
		req.UpdateMask.Paths = append(req.UpdateMask.Paths, "updated_by")
	}

	req.Data.Id = trans.Ptr(req.GetId())

	// 单租户：用户固定落在租户 0，不接受调用方指定租户
	req.Data.TenantId = nil

	var roleIds []uint32
	if len(req.Data.GetRoleIds()) > 0 {
		roleIds = req.Data.GetRoleIds()
	}
	if req.Data.RoleId != nil && *req.Data.RoleId > 0 {
		roleIds = append(roleIds, *req.Data.RoleId)
	}
	roleIds = sliceutil.Unique(roleIds)
	if len(roleIds) == 0 {
		s.log.Errorf(ctx, "role_ids is required")
		return nil, adminV1.ErrorBadRequest("role_ids is required")
	}

	queryString := fmt.Sprintf(`{"id__in": "[%s]", "type": "SYSTEM"}`,
		sliceutil.NumberSliceToString(roleIds),
	)
	roles, err := s.roleRepo.List(ctx, &paginationV1.PagingRequest{
		NoPaging: trans.Ptr(true),
		FilteringType: &paginationV1.PagingRequest_Query{
			Query: queryString,
		},
	})
	if err != nil {
		s.log.Errorf(ctx, "query roles err: %v", err)
		return nil, err
	}

	if len(roles.Items) != len(roleIds) {
		s.log.Errorf(ctx, "some roles not found, requested role ids: %v", roleIds)
		return nil, adminV1.ErrorBadRequest("some roles not found")
	}
	if len(roles.Items) == 0 {
		s.log.Errorf(ctx, "at least one role is required")
		return nil, adminV1.ErrorBadRequest("at least one role is required")
	}

	req.Data.RoleId = nil
	req.Data.RoleIds = roleIds

	// 必须在 userRepo.Update 之前比较：repo 会把 role_ids 从 mask 里剔除，
	// 更新成功后库里的集合已经是新值，再比就会恒等。
	// 列举失败按角色已变化处理，避免特权是否还在不确定时留下旧令牌。
	revokeForRoleChange := false
	if userUpdateRewritesRoles(req) {
		currentRoleIDs, listErr := s.userRepo.ListRoleIDsByUserID(ctx, req.GetId())
		if listErr != nil {
			s.log.Errorf(ctx, "list roles before update failed for user [%d]: %v", req.GetId(), listErr)
			revokeForRoleChange = true
			currentRoleIDs = nil // 现有集合不明：按全部是新加的角色做上限校验
		} else if !sameUint32Set(sliceutil.Unique(currentRoleIDs), roleIds) {
			revokeForRoleChange = true
		}

		// 授权上限只看新加的角色：页面保存会带上目标用户原有的角色，原样保留不算分配。
		added := newlyAdded(roleIds, currentRoleIDs)
		if err = ensureRolesWithinOperator(ctx, s.roleRepo, s.userRepo.ListRoleIDsByUserID, operator, added); err != nil {
			s.log.Errorf(ctx, "operator [%d] assign roles %v to user [%d] rejected: %v", operator.GetUserId(), added, req.GetId(), err)
			return nil, err
		}
	}

	// 停用、锁定、注销等非正常状态：旧令牌不能继续认证（认证器只核对 Redis，不查用户状态）。
	revokeForStatus := userUpdateDeactivates(req)

	// 密码和手机号都是登录凭证：操作人不能给权限比自己高的账号重置密码或改号。
	// 检查在仓储的更新事务里做：锁住目标用户行之后、任何写入之前，按锁内读到的角色
	// （也就是这次请求改写角色之前的角色）判定。不通过时这个请求的字段、角色、密码一律不写。
	guard := &data.UserUpdateGuard{
		CheckCredentialWrite: func(cctx context.Context, _ *ent.Tx, userID uint32, roleIDs []uint32) error {
			cerr := s.ensureCredentialWriteAllowed(cctx, operator, userID, roleIDs)
			if cerr != nil {
				s.log.Errorf(ctx, "operator [%d] write credential of user [%d] rejected: %v", operator.GetUserId(), userID, cerr)
			}
			return cerr
		},
	}

	resetsPassword := len(req.GetPassword()) > 0
	if resetsPassword {
		// 跨租户防护：非平台超级管理员重置密码前，校验目标用户真实租户。
		// 防 IDOR：必须用 DB 中的目标用户名，而非请求体里可伪造的 username，
		// 保证重置目标与请求 id 严格一致（避免 id=甲 + username=乙 实际重置了乙）。
		targetUser, gerr := s.userRepo.Get(ctx, &identityV1.GetUserRequest{
			QueryBy: &identityV1.GetUserRequest_Id{Id: req.GetId()},
		})
		if gerr != nil {
			return nil, gerr
		}
		if !operator.GetIsPlatformAdmin() && targetUser.GetTenantId() != operator.GetTenantId() {
			s.log.Errorf(ctx, "operator [%d] (tenant %d) has no permission to reset password of cross-tenant user [%d] (tenant %d)",
				operator.GetUserId(), operator.GetTenantId(), targetUser.GetId(), targetUser.GetTenantId())
			return nil, adminV1.ErrorForbidden("no permission to reset password of user in other tenant")
		}
		targetUsername := targetUser.GetUsername()
		targetID, newPassword := req.GetId(), req.GetPassword()
		// 口令策略在更新事务开始之前读好：事务里改密时不再用别的连接查 sys_config。
		ctx = s.userCredentialRepo.PreloadPasswordPolicy(ctx)
		// 改密和用户行、角色在同一个事务里：权限检查不通过或口令不合规时一起回滚。
		guard.ResetPassword = func(cctx context.Context, tx *ent.Tx) error {
			return s.userCredentialRepo.ResetCredentialWithTx(cctx, tx, targetID, &authenticationV1.ResetCredentialRequest{
				IdentityType:  authenticationV1.UserCredential_USERNAME,
				Identifier:    targetUsername,
				NewCredential: newPassword,
				NeedDecrypt:   true, // 前端 AES 密文传输，解密后才走复杂度/哈希
			})
		}
	}

	// 更新用户。
	outcome, err := s.userRepo.UpdateGuarded(ctx, req, guard)
	if err != nil {
		s.log.Error(ctx, err.Error())
		return nil, err
	}

	// 号码变了：旧号码建立的小程序会话不能再用，吊销该用户的 app 令牌。
	// 号码和库里一致（没有实际变化）时什么都不做。
	appRevokeFailed := false
	if outcome.MobileChanged {
		if rerr := s.authenticator.RevokeUserToken(ctx, authenticationV1.ClientType_app, req.GetId()); rerr != nil {
			s.log.Errorf(ctx, "revoke app tokens after mobile change failed for user [%d]: %v", req.GetId(), rerr)
			appRevokeFailed = true
		}
	}

	// 角色集合或状态已经落库。吊销失败只记日志：更新已提交，不能再把本次请求判失败。
	if revokeForRoleChange || revokeForStatus {
		if rerr := s.authenticator.RevokeUserTokenAllClientTypes(ctx, req.GetId()); rerr != nil {
			s.log.Errorf(ctx, "revoke tokens after role/status change failed for user [%d]: %v", req.GetId(), rerr)
		}
	}

	passwordRevokeFailed := false
	if resetsPassword {
		// 重置密码成功后吊销目标用户全部令牌，强制其重新登录，
		// 防止凭据已泄露的场景下旧令牌继续可用。
		if rerr := s.authenticator.RevokeUserTokenAllClientTypes(ctx, req.GetId()); rerr != nil {
			s.log.Errorf(ctx, "revoke tokens after password reset failed for user [%d]: %v", req.GetId(), rerr)
			passwordRevokeFailed = true
		}
	}

	if appRevokeFailed {
		// 号码已经改了，但旧的小程序令牌没清干净：已签发的 app 令牌在自己的有效期内还能用。
		return nil, adminV1.ErrorServiceUnavailable("mobile updated but session revocation failed")
	}
	if passwordRevokeFailed {
		// 密码已经重置、微信绑定已经删了，但旧令牌没清干净。
		return nil, adminV1.ErrorServiceUnavailable("password reset but session revocation failed")
	}

	return &emptypb.Empty{}, nil
}

// ensureCredentialWriteAllowed 写目标用户的登录凭证（重置密码、改手机号）前的权限上限检查：
// 目标用户当前的每个角色都必须在操作人的权限上限内。roleIDs 由仓储在锁住目标用户行之后读出。
func (s *UserService) ensureCredentialWriteAllowed(ctx context.Context, operator *authenticationV1.UserTokenPayload,
	targetUserID uint32, roleIDs []uint32) error {
	return ensureTargetWithinOperator(ctx, s.roleRepo, s.userRepo.ListRoleIDsByUserID, operator, targetUserID, roleIDs)
}

// ensureTargetWithinOperator 目标用户的权限上限检查：目标用户当前的每个角色都必须在操作人的权限上限内。
// 写目标用户的登录凭证（重置密码、改手机号）和解除他的微信绑定之前都要过这一关。
func ensureTargetWithinOperator(ctx context.Context, roleRepo *data.RoleRepo, listRoles operatorRoleIDsFunc,
	operator *authenticationV1.UserTokenPayload, targetUserID uint32, roleIDs []uint32) error {
	// 平台管理员不受限；操作人改自己的账号不存在越权。
	if operator.GetIsPlatformAdmin() || targetUserID == operator.GetUserId() {
		return nil
	}
	err := ensureRolesWithinOperator(ctx, roleRepo, listRoles, operator, sliceutil.Unique(roleIDs))
	if adminV1.IsForbidden(err) {
		// 上限检查的文案是按「分配角色」写的，这里换成对目标用户的说法。
		return adminV1.ErrorForbidden("target user has higher authority than you")
	}
	return err
}

// userUpdateRewritesRoles 与 user_repo.Update 的关联重建条件对齐。
// 无 mask 或空 paths 视为全量更新，会重建角色；有 mask 时只有 role_ids / role_id 会重建。
// 调用点须在追加 updated_by 之后、userRepo.Update 之前：repo 会从 mask 里剔除这些路径。
// 管理端 HTTP 的 protojson 会把 roleIds 规范成 role_ids，这里按 repo 实际识别的 proto 名判断。
func userUpdateRewritesRoles(req *identityV1.UpdateUserRequest) bool {
	fm := req.GetUpdateMask()
	if fm == nil || len(fm.GetPaths()) == 0 {
		return true
	}
	for _, p := range fm.GetPaths() {
		switch p {
		case "role_ids", "role_id":
			return true
		}
	}
	return false
}

// userUpdateDeactivates 判断本次更新是否把用户写成非 NORMAL 状态。
// 无 mask 或空 paths 视为全量更新；有 mask 时只看 status 是否在 mask 里。
func userUpdateDeactivates(req *identityV1.UpdateUserRequest) bool {
	if req.GetData().Status == nil || req.GetData().GetStatus() == identityV1.User_NORMAL {
		return false
	}
	fm := req.GetUpdateMask()
	if fm == nil || len(fm.GetPaths()) == 0 {
		return true
	}
	for _, p := range fm.GetPaths() {
		if p == "status" {
			return true
		}
	}
	return false
}

// sameUint32Set 忽略顺序，按多重集合比较。角色 ID 在写入前已经去重。
func sameUint32Set(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[uint32]int, len(a))
	for _, id := range a {
		seen[id]++
	}
	for _, id := range b {
		n := seen[id]
		if n == 0 {
			return false
		}
		seen[id] = n - 1
	}
	return true
}

// requireUserInScopeForUpdate Update 的范围检查：已存在但越界回 404；
// allow_missing 且目标不存在（未过滤的存在性）时放行，由仓储创建。
func (s *UserService) requireUserInScopeForUpdate(ctx context.Context, req *identityV1.UpdateUserRequest) error {
	if req.GetAllowMissing() {
		exist, err := s.userRepo.UserExists(ctx, &identityV1.UserExistsRequest{
			QueryBy: &identityV1.UserExistsRequest_Id{Id: req.GetId()},
		})
		if err != nil {
			s.log.Errorf(ctx, "check user exists failed: %v", err)
			return err
		}
		if !exist.GetExist() {
			return nil
		}
	}
	_, err := s.userRepo.Get(data.WithDataScope(ctx), &identityV1.GetUserRequest{
		QueryBy: &identityV1.GetUserRequest_Id{Id: req.GetId()},
	})
	return err
}

func (s *UserService) Delete(ctx context.Context, req *identityV1.DeleteUserRequest) (*emptypb.Empty, error) {
	// 获取操作人信息
	operator, err := auth.FromContext(ctx)
	if err != nil {
		return nil, err
	}

	// 获取操作者的用户信息
	_, err = s.userRepo.Get(ctx, &identityV1.GetUserRequest{
		QueryBy: &identityV1.GetUserRequest_Id{
			Id: operator.UserId,
		},
	})
	if err != nil {
		return nil, err
	}

	// 获取将被删除的用户信息（按数据范围，越界回 404）
	target, err := s.userRepo.Get(data.WithDataScope(ctx), &identityV1.GetUserRequest{
		QueryBy: &identityV1.GetUserRequest_Id{
			Id: req.GetId(),
		},
	})
	if err != nil {
		return nil, err
	}

	// 禁止删除默认超级管理员：初始化时创建的平台级 admin（恒为 id=1，
	// 即便后续改名也由 id 兜底保护）。误删会导致系统失去超级管理员且无法自动重建。
	if target.GetId() == 1 ||
		(target.GetUsername() == constants.DefaultAdminUserName && target.GetTenantId() == 0) {
		s.log.Errorf(ctx, "operator [%d] attempted to delete default admin user [%d]",
			operator.GetUserId(), target.GetId())
		return nil, adminV1.ErrorBadRequest("default admin cannot be deleted")
	}

	// 禁止删除自己：误删自身账号将导致当前会话立即失去管理能力。
	if target.GetId() == operator.GetUserId() {
		s.log.Errorf(ctx, "operator [%d] attempted to delete self", operator.GetUserId())
		return nil, adminV1.ErrorBadRequest("cannot delete yourself")
	}

	// 删除用户
	if err = s.userRepo.Delete(ctx, req); err != nil {
		return nil, err
	}

	// 删除已提交，旧令牌必须失效。吊销失败只记日志，不把已完成的删除判失败。
	if rerr := s.authenticator.RevokeUserTokenAllClientTypes(ctx, target.GetId()); rerr != nil {
		s.log.Errorf(ctx, "revoke tokens after delete failed for user [%d]: %v", target.GetId(), rerr)
	}

	return &emptypb.Empty{}, nil
}

func (s *UserService) UserExists(ctx context.Context, req *identityV1.UserExistsRequest) (*identityV1.UserExistsResponse, error) {
	return s.userRepo.UserExists(ctx, req)
}

// EditUserPassword 修改用户密码
func (s *UserService) EditUserPassword(ctx context.Context, req *identityV1.EditUserPasswordRequest) (*emptypb.Empty, error) {
	// 获取操作人信息
	operator, err := auth.FromContext(ctx)
	if err != nil {
		return nil, err
	}

	// 获取目标用户信息（按数据范围，越界回 404）
	u, err := s.userRepo.Get(data.WithDataScope(ctx), &identityV1.GetUserRequest{
		QueryBy: &identityV1.GetUserRequest_Id{
			Id: req.GetUserId(),
		},
	})
	if err != nil {
		return nil, err
	}

	// 跨租户防护：非平台超级管理员只能重置本租户用户的密码
	if !operator.GetIsPlatformAdmin() && u.GetTenantId() != operator.GetTenantId() {
		s.log.Errorf(ctx, "operator [%d] (tenant %d) has no permission to reset password of cross-tenant user [%d] (tenant %d)",
			operator.GetUserId(), operator.GetTenantId(), u.GetId(), u.GetTenantId())
		return nil, adminV1.ErrorForbidden("no permission to reset password of user in other tenant")
	}

	// 权限上限：不能给权限比自己高的账号重置密码。检查和改密在同一个事务里，
	// 锁住目标用户行之后按锁内读到的角色判定，中间不会被并发的角色变更插进来。
	if err = s.userCredentialRepo.ResetCredentialGuarded(ctx, u.GetId(), &authenticationV1.ResetCredentialRequest{
		IdentityType:  authenticationV1.UserCredential_USERNAME,
		Identifier:    u.GetUsername(),
		NewCredential: req.GetNewPassword(),
		NeedDecrypt:   true, // 前端 AES 密文传输，解密后才走复杂度/哈希
	}, func(cctx context.Context, _ *ent.Tx, roleIDs []uint32) error {
		return s.ensureCredentialWriteAllowed(cctx, operator, u.GetId(), roleIDs)
	}); err != nil {
		s.log.Errorf(ctx, "reset user password err: %v", err)
		return nil, err
	}

	// 重置密码成功后吊销目标用户全部令牌，强制其重新登录。密码和微信绑定的删除已经提交；
	// 吊销失败时返回 503：已签发的令牌在自己的有效期内还能用。
	if err = s.authenticator.RevokeUserTokenAllClientTypes(ctx, u.GetId()); err != nil {
		s.log.Errorf(ctx, "revoke tokens after password reset failed for user [%d]: %v", u.GetId(), err)
		return nil, adminV1.ErrorServiceUnavailable("password reset but session revocation failed")
	}

	return &emptypb.Empty{}, nil
}

// createDefaultUser 创建默认用户，即超级用户
func (s *UserService) createDefaultUser(ctx context.Context) error {
	var err error

	// 创建默认用户
	var defaultUserID uint32
	for _, user := range constants.DefaultUsers {
		var created *identityV1.User
		if created, err = s.userRepo.Create(ctx, &identityV1.CreateUserRequest{
			Data: user,
		}); err != nil {
			s.log.Errorf(ctx, "create default user err: %v", err)
			return err
		}
		if created.GetId() > 0 {
			defaultUserID = created.GetId()
		}
	}

	// 创建默认用户凭证（绑定实际生成的用户 ID，auto_increment 不保证首行是 1）
	if err = s.createDefaultUserCredentials(ctx, defaultUserID); err != nil {
		return err
	}

	switch constants.DefaultUserTenantRelationType {
	default:
		fallthrough
	case constants.UserTenantRelationOneToOne:
		// 创建默认用户角色关联关系
		for _, userRole := range constants.DefaultUserRoles {
			if err = s.userRepo.AssignUserRole(ctx, userRole); err != nil {
				s.log.Errorf(ctx, "create default user role relation err: %v", err)
				return err
			}
		}

	case constants.UserTenantRelationOneToMany:
		// 创建默认用户租户关联关系
		for _, membership := range constants.DefaultMemberships {
			if err = s.membershipRepo.AssignTenantMembershipWith(ctx, membership); err != nil {
				s.log.Errorf(ctx, "create default user membership err: %v", err)
				return err
			}
		}
	}

	return err
}

// createDefaultUserCredentials 种入默认用户凭证。
// overrideUserID > 0 时覆盖种子里的 UserId（初始化路径绑定实际生成的用户 ID）；
// 为 0 时保留种子原值（自愈补种路径，此时 admin 即空表首行 id=1）。
func (s *UserService) createDefaultUserCredentials(ctx context.Context, overrideUserID uint32) error {
	initialPassword := os.Getenv(constants.AdminInitialPasswordEnv)
	if initialPassword == "" {
		s.log.Warnf(ctx, "⚠️ 未设置 %s，admin 初始密码使用仓库公开的默认值，部署后必须立即修改",
			constants.AdminInitialPasswordEnv)
	}
	for _, userCredential := range constants.DefaultUserCredentials {
		credential := proto.Clone(userCredential).(*authenticationV1.UserCredential)
		if overrideUserID > 0 {
			credential.UserId = trans.Ptr(overrideUserID)
		}
		if initialPassword != "" {
			credential.Credential = trans.Ptr(initialPassword)
		}
		if err := s.userCredentialRepo.Create(ctx, &authenticationV1.CreateUserCredentialRequest{
			Data: credential,
		}); err != nil {
			s.log.Errorf(ctx, "create default user credential err: %v", err)
			return err
		}
	}
	return nil
}
