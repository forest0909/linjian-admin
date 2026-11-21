// Package phone 手机号的规范和打码。
//
// 手机号是登录凭证（小程序一键登录按 sys_users.mobile 精确匹配），
// 短信验证、落库、微信匹配都用这里的同一个规范函数。第一期只接受中国大陆 11 位号码。
package phone

import (
	"regexp"
	"strings"
)

var cnMobilePattern = regexp.MustCompile(`^1[3-9]\d{9}$`)

// NormalizeCNMobile 去掉首尾空格和 +86 前缀后要求是中国大陆 11 位手机号，返回 11 位纯数字。
// 不符合时 ok 为 false。
func NormalizeCNMobile(raw string) (mobile string, ok bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimSpace(strings.TrimPrefix(s, "+86"))
	if !cnMobilePattern.MatchString(s) {
		return "", false
	}
	return s, true
}

// Mask 日志用的手机号打码：保留前 3 后 4（138****0000）。不足 8 位的全部打码。
func Mask(mobile string) string {
	s := strings.TrimSpace(mobile)
	if len(s) < 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:3] + "****" + s[len(s)-4:]
}

// MaskEmail 日志用的邮箱打码：本地部分只留首字符（a***@example.com）。
func MaskEmail(email string) string {
	s := strings.TrimSpace(email)
	at := strings.LastIndex(s, "@")
	if at <= 0 {
		return strings.Repeat("*", len(s))
	}
	return s[:1] + "***" + s[at:]
}
