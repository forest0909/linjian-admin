package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeWechatYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "wechat.yaml"), []byte(content), 0o600))
	return dir
}

func TestLoadMiniappConfig(t *testing.T) {
	// 环境变量会盖过文件，先清掉外面带进来的。
	for _, name := range []string{envMiniappAppID, envMiniappAppSecret, envMiniappMock} {
		t.Setenv(name, "")
	}

	// 文件不存在：未启用，不报错。
	cfg, err := LoadMiniappConfig(t.TempDir())
	require.NoError(t, err)
	require.False(t, cfg.Enabled())
	require.NoError(t, cfg.Validate(false))

	// 仓库里的默认文件：未启用。
	cfg, err = LoadMiniappConfig("../../configs")
	require.NoError(t, err)
	require.Equal(t, MiniappConfig{}, cfg)

	// 文件在但写坏了：报错，不当成「没配」。
	_, err = LoadMiniappConfig(writeWechatYAML(t, "miniapp: [unclosed"))
	require.Error(t, err)

	// 读文件。
	dir := writeWechatYAML(t, "miniapp:\n  app_id: \" wx-file \"\n  app_secret: file-secret\n  mock: false\n")
	cfg, err = LoadMiniappConfig(dir)
	require.NoError(t, err)
	require.Equal(t, MiniappConfig{AppID: "wx-file", AppSecret: "file-secret"}, cfg)

	// 环境变量优先于文件。
	t.Setenv(envMiniappAppID, "wx-env")
	t.Setenv(envMiniappAppSecret, "env-secret")
	cfg, err = LoadMiniappConfig(dir)
	require.NoError(t, err)
	require.Equal(t, MiniappConfig{AppID: "wx-env", AppSecret: "env-secret"}, cfg)

	t.Setenv(envMiniappMock, "true")
	cfg, err = LoadMiniappConfig(dir)
	require.NoError(t, err)
	require.True(t, cfg.Mock)
}

func TestMiniappConfigValidate(t *testing.T) {
	cases := []struct {
		name      string
		cfg       MiniappConfig
		mockBuild bool
		wantErr   bool
	}{
		{"未启用", MiniappConfig{}, false, false},
		{"配齐", MiniappConfig{AppID: "wx", AppSecret: "s"}, false, false},
		{"配了 app_id 没配 Secret", MiniappConfig{AppID: "wx"}, false, true},
		{"正式构建 mock 且没有 Secret", MiniappConfig{AppID: "wx", Mock: true}, false, true},
		{"正式构建 mock 且有 Secret", MiniappConfig{AppID: "wx", AppSecret: "s", Mock: true}, false, true},
		{"正式构建 mock 且没有 app_id", MiniappConfig{Mock: true}, false, true},
		{"开发构建 mock 不需要 Secret", MiniappConfig{AppID: "wx", Mock: true}, true, false},
		{"开发构建没开 mock 仍要 Secret", MiniappConfig{AppID: "wx"}, true, true},
	}
	for _, c := range cases {
		err := c.cfg.Validate(c.mockBuild)
		require.Equal(t, c.wantErr, err != nil, "%s: %v", c.name, err)
		if err != nil {
			// 报错里不带 Secret。
			require.NotContains(t, err.Error(), "s3cr3t")
		}
	}

	var nilRuntime *MiniappRuntime
	require.False(t, nilRuntime.Enabled())
	require.False(t, (&MiniappRuntime{AppID: "wx"}).Enabled(), "没有客户端不算启用")
}
