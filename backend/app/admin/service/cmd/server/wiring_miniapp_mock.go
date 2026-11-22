//go:build miniapp_mock

package main

import (
	"github.com/redis/go-redis/v9"
	"go-wind-admin/third_party/kratos-bootstrap/bootstrap"

	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"

	"go-wind-admin/pkg/wechat"
)

// newMiniappRuntime 带 miniapp_mock 标签的开发构建：配置里 mock: true 时装配不调微信的 mock 客户端。
// 正式制品（make build、Docker 镜像）不带这个标签，用 wiring_miniapp.go。
// 这种构建只连本机的开发库和开发 Redis：mock 下任何人都能用 mock_phone_<号码> 登录该号码的账号。
func newMiniappRuntime(ctx *bootstrap.Context, cfg service.MiniappConfig, rdb *redis.Client) (*service.MiniappRuntime, error) {
	if err := cfg.Validate(true); err != nil {
		return nil, err
	}
	if !cfg.Enabled() {
		return nil, nil
	}
	rt := &service.MiniappRuntime{AppID: cfg.AppID, Quota: data.NewMiniappQuota(ctx, rdb)}
	if cfg.Mock {
		ctx.NewLoggerHelper("miniapp/wiring").Warnf(ctx.Context(),
			"⚠️ 微信小程序登录正在使用 MOCK 客户端：不调微信，mock_openid_<串> / mock_phone_<号码> 直接通过。"+
				"只能用于本机开发，不要连生产库。")
		rt.Client = wechat.NewMockMiniappClient()
		return rt, nil
	}
	rt.Client = wechat.NewMiniappClient(wechat.Config{AppID: cfg.AppID, AppSecret: cfg.AppSecret}, rdb)
	return rt, nil
}
