package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"go-wind-admin/app/admin/service/internal/data"

	"go-wind-admin/pkg/wechat"
)

// 环境变量，优先于 wechat.yaml。
const (
	envMiniappAppID     = "GWA_WECHAT_MINIAPP_APP_ID"
	envMiniappAppSecret = "GWA_WECHAT_MINIAPP_APP_SECRET"
	// envMiniappMock 开发用：run-miniapp-mock 靠它打开 mock，不用改配置文件。
	// 和文件里的 mock: true 一样，正式二进制读到 true 会拒绝启动。
	envMiniappMock = "GWA_WECHAT_MINIAPP_MOCK"
)

// MiniappConfig 是 configs/wechat.yaml 的内存形态。
type MiniappConfig struct {
	AppID     string
	AppSecret string
	// Mock 用不调微信的 mock 客户端。只在带 miniapp_mock 编译标签的开发构建里有效。
	Mock bool
}

type miniappFile struct {
	Miniapp struct {
		AppID     string `yaml:"app_id"`
		AppSecret string `yaml:"app_secret"`
		Mock      bool   `yaml:"mock"`
	} `yaml:"miniapp"`
}

// LoadMiniappConfig 读取 <configDir>/wechat.yaml，再用环境变量覆盖。
// 文件不存在按未启用处理；文件在但解析失败时返回错误，避免把写坏的配置当成「没配」。
func LoadMiniappConfig(configDir string) (MiniappConfig, error) {
	var file miniappFile
	path := filepath.Join(configDir, "wechat.yaml")
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err = yaml.Unmarshal(raw, &file); err != nil {
			return MiniappConfig{}, fmt.Errorf("parse %s: %w", path, err)
		}
	case !os.IsNotExist(err):
		return MiniappConfig{}, fmt.Errorf("read %s: %w", path, err)
	}

	cfg := MiniappConfig{
		AppID:     strings.TrimSpace(file.Miniapp.AppID),
		AppSecret: strings.TrimSpace(file.Miniapp.AppSecret),
		Mock:      file.Miniapp.Mock,
	}
	if v := strings.TrimSpace(os.Getenv(envMiniappAppID)); v != "" {
		cfg.AppID = v
	}
	if v := strings.TrimSpace(os.Getenv(envMiniappAppSecret)); v != "" {
		cfg.AppSecret = v
	}
	if v := strings.TrimSpace(os.Getenv(envMiniappMock)); v != "" {
		cfg.Mock = strings.EqualFold(v, "true") || v == "1"
	}
	return cfg, nil
}

// Enabled 填了 app_id 才算启用。
func (c MiniappConfig) Enabled() bool {
	return c.AppID != ""
}

// Validate 启动时的配置检查。mockBuild 表示当前二进制带 miniapp_mock 编译标签。
//   - 正式二进制读到 mock: true 拒绝启动，不管有没有配 Secret。
//   - 配了 app_id 没配 app_secret（且不是开着 mock 的开发构建）拒绝启动。
func (c MiniappConfig) Validate(mockBuild bool) error {
	if c.Mock && !mockBuild {
		return errors.New("wechat.yaml: miniapp.mock is true, but this binary was built without the miniapp_mock tag; " +
			"mock is for local development only (make -C app/admin/service run-miniapp-mock)")
	}
	if c.Enabled() && c.AppSecret == "" && !c.Mock {
		return errors.New("wechat.yaml: miniapp.app_id is set but miniapp.app_secret is empty " +
			"(set it in the file or via " + envMiniappAppSecret + ")")
	}
	return nil
}

// MiniappRuntime 小程序登录在运行时要用的三样东西。nil 或 AppID 为空表示未启用。
type MiniappRuntime struct {
	AppID  string
	Client wechat.MiniappClient
	Quota  *data.MiniappQuota
}

// Enabled 未启用时一切 app 签发都拒绝，包括 /admin/v1/login 带 client_type=app。
func (m *MiniappRuntime) Enabled() bool {
	return m != nil && m.AppID != "" && m.Client != nil
}
