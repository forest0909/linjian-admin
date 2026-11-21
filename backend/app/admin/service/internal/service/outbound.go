package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go-wind-admin/third_party/go-utils/mailer"
	"go-wind-admin/third_party/go-utils/notify"
	"go-wind-admin/third_party/go-utils/notify/sms"
	"gopkg.in/yaml.v3"
)

// ErrOutboundNotConfigured 表示 mail.yaml 里这一路没填，一次都没向对端拨号。
var ErrOutboundNotConfigured = errors.New("outbound channel is not configured")

// OutboundConfig 是 configs/mail.yaml 的内存形态。
type OutboundConfig struct {
	SMTP mailer.SmtpConfig
	SMS  sms.Config
}

type outboundFile struct {
	SMTP struct {
		Host     string `yaml:"host"`
		Port     uint32 `yaml:"port"`
		Username string `yaml:"username"`
		Password string `yaml:"password"`
		From     string `yaml:"from"`
		FromName string `yaml:"from_name"`
		TLS      string `yaml:"tls"`
	} `yaml:"smtp"`
	SMS struct {
		Provider     string `yaml:"provider"`
		AccessKeyID  string `yaml:"access_key_id"`
		AccessSecret string `yaml:"access_secret"`
		SignName     string `yaml:"sign_name"`
		TemplateCode string `yaml:"template_code"`
		AppID        string `yaml:"app_id"`
		Region       string `yaml:"region"`
	} `yaml:"sms"`
}

// ResolveConfigDir 取启动参数里的配置目录，与 bootstrap 的 -c/--conf 对齐。
// 没传时依次试 ./configs 和 ../../configs（cmd/server 的默认相对路径）。
func ResolveConfigDir() string {
	args := os.Args
	for i, arg := range args {
		switch {
		case arg == "-c" || arg == "--conf":
			if i+1 < len(args) {
				return args[i+1]
			}
		case strings.HasPrefix(arg, "-c="):
			return strings.TrimPrefix(arg, "-c=")
		case strings.HasPrefix(arg, "--conf="):
			return strings.TrimPrefix(arg, "--conf=")
		}
	}
	for _, dir := range []string{"./configs", "../../configs"} {
		info, err := os.Stat(dir)
		if err == nil && info.IsDir() {
			return dir
		}
	}
	return "./configs"
}

// LoadOutboundConfig 读取 <configDir>/mail.yaml。
// 文件不存在时返回空配置和 nil：发送时再报未配置，不拦住服务启动。
// 文件在但解析失败时返回错误，避免把写坏的口令文件当成「没配」。
func LoadOutboundConfig(configDir string) (OutboundConfig, error) {
	path := filepath.Join(configDir, "mail.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return OutboundConfig{}, nil
		}
		return OutboundConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	var file outboundFile
	if err = yaml.Unmarshal(raw, &file); err != nil {
		return OutboundConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return OutboundConfig{
		SMTP: mailer.SmtpConfig{
			Host:     strings.TrimSpace(file.SMTP.Host),
			Port:     file.SMTP.Port,
			Username: file.SMTP.Username,
			Password: file.SMTP.Password,
			From:     strings.TrimSpace(file.SMTP.From),
			FromName: strings.TrimSpace(file.SMTP.FromName),
			TlsMode:  strings.TrimSpace(file.SMTP.TLS),
		},
		SMS: sms.Config{
			Provider:     strings.TrimSpace(file.SMS.Provider),
			AccessKeyID:  strings.TrimSpace(file.SMS.AccessKeyID),
			AccessSecret: strings.TrimSpace(file.SMS.AccessSecret),
			SignName:     strings.TrimSpace(file.SMS.SignName),
			TemplateCode: strings.TrimSpace(file.SMS.TemplateCode),
			AppID:        strings.TrimSpace(file.SMS.AppID),
			Region:       strings.TrimSpace(file.SMS.Region),
		},
	}, nil
}

// scrubbedError 发送失败的错误：文本里的收件地址和验证码已经清掉，可以直接进日志。
// SMTP 的拒收错误会带原始收件人，短信服务商的应答可能回显请求内容。
// Unwrap 保留原错误，errors.Is 照常可用；不要再打印 Unwrap 出来的错误。
type scrubbedError struct {
	msg   string
	cause error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.cause }

// scrubSendError 把错误文本里出现的 secrets（收件地址、验证码）换成占位符，大小写不敏感。
func scrubSendError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		msg = regexp.MustCompile("(?i)"+regexp.QuoteMeta(secret)).ReplaceAllString(msg, "[redacted]")
	}
	return &scrubbedError{msg: msg, cause: err}
}

// OutboundSender 按 mail.yaml 发验证码邮件和短信。配置在进程启动时读一次。
type OutboundSender struct {
	cfg OutboundConfig
}

func NewOutboundSender(cfg OutboundConfig) *OutboundSender {
	return &OutboundSender{cfg: cfg}
}

// SendMail 发一封纯文本邮件。host 或 port 为空时返回 ErrOutboundNotConfigured。
func (s *OutboundSender) SendMail(ctx context.Context, to, subject, body string) error {
	if s == nil || s.cfg.SMTP.Host == "" || s.cfg.SMTP.Port == 0 {
		return ErrOutboundNotConfigured
	}
	if !mailer.IsSupportedTlsMode(s.cfg.SMTP.TlsMode) {
		return fmt.Errorf("%w: smtp.tls %q is not one of NONE/START_TLS/SSL", ErrOutboundNotConfigured, s.cfg.SMTP.TlsMode)
	}
	if err := mailer.SendMail(ctx, s.cfg.SMTP, []string{to}, subject, body); err != nil {
		return scrubSendError(fmt.Errorf("send mail failed: %w", err), to)
	}
	return nil
}

// SMSConfigured 短信渠道是否配置齐全。不拨号。
func (s *OutboundSender) SMSConfigured() bool {
	if s == nil || s.cfg.SMS.Provider == "" {
		return false
	}
	svc, err := sms.New(nil, s.cfg.SMS)
	if err != nil {
		return false
	}
	return svc.Precheck() == nil
}

// SendSMS 发一条验证码短信。provider 留空，或必填项不全时返回 ErrOutboundNotConfigured。
func (s *OutboundSender) SendSMS(ctx context.Context, phone, code string) error {
	if s == nil || s.cfg.SMS.Provider == "" {
		return ErrOutboundNotConfigured
	}
	svc, err := sms.New(nil, s.cfg.SMS)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOutboundNotConfigured, err)
	}
	if err = svc.Precheck(); err != nil {
		return fmt.Errorf("%w: %v", ErrOutboundNotConfigured, err)
	}
	_, err = svc.Send(ctx, &notify.Message{
		Target: phone,
		Params: map[string]string{"code": code},
	})
	if err != nil {
		if errors.Is(err, notify.ErrNotConfigured) {
			return scrubSendError(fmt.Errorf("%w: %v", ErrOutboundNotConfigured, err), phone, code)
		}
		return scrubSendError(fmt.Errorf("send sms failed: %w", err), phone, code)
	}
	return nil
}
