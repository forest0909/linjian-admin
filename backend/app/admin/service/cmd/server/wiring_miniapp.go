//go:build !miniapp_mock

package main

import (
	"github.com/redis/go-redis/v9"
	"go-wind-admin/third_party/kratos-bootstrap/bootstrap"

	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"

	"go-wind-admin/pkg/wechat"
)

// newMiniappRuntime 正式构建的小程序登录装配：只有调用微信的真实客户端。
// 这份二进制里没有 mock 实现；配置里读到 mock: true 直接拒绝启动，不管有没有配 Secret。
// 带 miniapp_mock 标签的开发构建用 wiring_miniapp_mock.go 里的同名函数。
func newMiniappRuntime(ctx *bootstrap.Context, cfg service.MiniappConfig, rdb *redis.Client) (*service.MiniappRuntime, error) {
	if err := cfg.Validate(false); err != nil {
		return nil, err
	}
	if !cfg.Enabled() {
		return nil, nil
	}
	return &service.MiniappRuntime{
		AppID:  cfg.AppID,
		Client: wechat.NewMiniappClient(wechat.Config{AppID: cfg.AppID, AppSecret: cfg.AppSecret}, rdb),
		Quota:  data.NewMiniappQuota(ctx, rdb),
	}, nil
}
