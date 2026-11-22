package wechat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	defaultBaseURL = "https://api.weixin.qq.com"

	// requestTimeout 每次调微信的超时。
	requestTimeout = 5 * time.Second

	// accessTokenKeyPrefix access_token 在 Redis 里的键前缀，后面接 AppID。
	accessTokenKeyPrefix = "gowind:wechat:miniapp:access_token:"

	// accessTokenSafetyMargin access_token 的缓存比微信给的有效期短这么多。
	accessTokenSafetyMargin = 300 * time.Second

	// maxResponseBytes 微信的应答都很小，读到这个长度为止。
	maxResponseBytes = 64 << 10
)

// 微信的错误码。
const (
	errCodeInvalidCode  = 40029 // code 无效或已过期
	errCodeCodeUsed     = 40163 // code 已被使用
	errCodeTokenInvalid = 40001 // access_token 无效
	errCodeTokenExpired = 42001 // access_token 过期
)

// Config 真实客户端的配置。
type Config struct {
	AppID     string
	AppSecret string
	// BaseURL 微信接口地址，留空用正式地址。测试里指向 httptest 假服务端。
	BaseURL string
	// HTTPClient 留空用带 5 秒超时的默认客户端。
	HTTPClient *http.Client
}

// client 真实实现。对 mock_ 开头的 code 没有任何特殊处理。
type client struct {
	cfg  Config
	http *http.Client
	rdb  redis.Cmdable
}

// NewMiniappClient 创建调用微信服务端的客户端。rdb 用来缓存 access_token。
func NewMiniappClient(cfg Config, rdb redis.Cmdable) MiniappClient {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	return &client{cfg: cfg, http: httpClient, rdb: rdb}
}

// wxStatus 微信应答里公共的错误码部分。errmsg 不解析：它不进日志，也不往外传。
type wxStatus struct {
	ErrCode int `json:"errcode"`
}

// do 发请求并把应答解析进 out。任何失败都只返回 *Error：
// *url.Error 带完整 URL（里面有 Secret 或 access_token），在这里截断，不往外传。
func (c *client) do(ctx context.Context, api string, req *http.Request, out any) *Error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	resp, err := c.http.Do(req.WithContext(ctx))
	if err != nil {
		return &Error{API: api, Kind: KindUnavailable}
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil || resp.StatusCode != http.StatusOK {
		return &Error{API: api, Kind: KindUnavailable}
	}
	if err = json.Unmarshal(body, out); err != nil {
		return &Error{API: api, Kind: KindUnavailable}
	}
	return nil
}

// codeError 把微信的错误码归成两类。
func codeError(api string, errCode int) *Error {
	kind := KindUnavailable
	if errCode == errCodeInvalidCode || errCode == errCodeCodeUsed {
		kind = KindCodeInvalid
	}
	return &Error{API: api, ErrCode: errCode, Kind: kind}
}

// Code2Session 调 sns/jscode2session。session_key 不保存。
func (c *client) Code2Session(ctx context.Context, code string) (string, error) {
	q := url.Values{}
	q.Set("appid", c.cfg.AppID)
	q.Set("secret", c.cfg.AppSecret)
	q.Set("js_code", code)
	q.Set("grant_type", "authorization_code")
	req, err := http.NewRequest(http.MethodGet, c.cfg.BaseURL+"/sns/jscode2session?"+q.Encode(), nil)
	if err != nil {
		return "", &Error{API: APICode2Session, Kind: KindUnavailable}
	}

	var out struct {
		wxStatus
		OpenID string `json:"openid"`
	}
	if e := c.do(ctx, APICode2Session, req, &out); e != nil {
		return "", e
	}
	if out.ErrCode != 0 {
		return "", codeError(APICode2Session, out.ErrCode)
	}
	if out.OpenID == "" {
		return "", &Error{API: APICode2Session, Kind: KindCodeInvalid}
	}
	return out.OpenID, nil
}

// GetPhoneNumber 调 wxa/business/getuserphonenumber，校验应答齐全、水印的 AppID 是自己的。
// 只在微信明确说 access_token 无效（40001、42001）时删缓存、重取、重试一次；网络超时不重试。
func (c *client) GetPhoneNumber(ctx context.Context, phoneCode string) (*Phone, error) {
	phone, e := c.getPhoneNumberOnce(ctx, phoneCode)
	if e != nil && e.API == APIGetPhoneNumber && (e.ErrCode == errCodeTokenInvalid || e.ErrCode == errCodeTokenExpired) {
		if derr := c.rdb.Del(ctx, c.accessTokenKey()).Err(); derr != nil {
			return nil, &Error{API: APIStableToken, Kind: KindUnavailable}
		}
		phone, e = c.getPhoneNumberOnce(ctx, phoneCode)
	}
	if e != nil {
		return nil, e
	}
	return phone, nil
}

func (c *client) getPhoneNumberOnce(ctx context.Context, phoneCode string) (*Phone, *Error) {
	token, e := c.accessToken(ctx)
	if e != nil {
		return nil, e
	}

	payload, err := json.Marshal(map[string]string{"code": phoneCode})
	if err != nil {
		return nil, &Error{API: APIGetPhoneNumber, Kind: KindUnavailable}
	}
	q := url.Values{}
	q.Set("access_token", token)
	req, err := http.NewRequest(http.MethodPost, c.cfg.BaseURL+"/wxa/business/getuserphonenumber?"+q.Encode(), bytes.NewReader(payload))
	if err != nil {
		return nil, &Error{API: APIGetPhoneNumber, Kind: KindUnavailable}
	}
	req.Header.Set("Content-Type", "application/json")

	var out struct {
		wxStatus
		PhoneInfo *struct {
			PurePhoneNumber string `json:"purePhoneNumber"`
			CountryCode     string `json:"countryCode"`
			Watermark       *struct {
				AppID string `json:"appid"`
			} `json:"watermark"`
		} `json:"phone_info"`
	}
	if e = c.do(ctx, APIGetPhoneNumber, req, &out); e != nil {
		return nil, e
	}
	if out.ErrCode != 0 {
		return nil, codeError(APIGetPhoneNumber, out.ErrCode)
	}
	info := out.PhoneInfo
	if info == nil || info.PurePhoneNumber == "" || info.CountryCode == "" || info.Watermark == nil {
		return nil, &Error{API: APIGetPhoneNumber, Kind: KindCodeInvalid}
	}
	// 水印不是自己的 AppID：这个手机号 code 是给别的小程序的。
	if info.Watermark.AppID != c.cfg.AppID {
		return nil, &Error{API: APIGetPhoneNumber, Kind: KindCodeInvalid}
	}
	return &Phone{CountryCode: info.CountryCode, PurePhoneNumber: info.PurePhoneNumber}, nil
}

func (c *client) accessTokenKey() string {
	return accessTokenKeyPrefix + c.cfg.AppID
}

// accessToken 取 access_token：先看 Redis，没有再调 cgi-bin/stable_token（固定用普通模式）。
// 普通模式在有效期内重复调用返回同一个 token，多实例同时来取也不会互相作废，不需要分布式锁。
func (c *client) accessToken(ctx context.Context) (string, *Error) {
	cached, err := c.rdb.Get(ctx, c.accessTokenKey()).Result()
	if err == nil && cached != "" {
		return cached, nil
	}
	if err != nil && !errors.Is(err, redis.Nil) {
		return "", &Error{API: APIStableToken, Kind: KindUnavailable}
	}

	payload, err := json.Marshal(map[string]any{
		"grant_type":    "client_credential",
		"appid":         c.cfg.AppID,
		"secret":        c.cfg.AppSecret,
		"force_refresh": false,
	})
	if err != nil {
		return "", &Error{API: APIStableToken, Kind: KindUnavailable}
	}
	req, err := http.NewRequest(http.MethodPost, c.cfg.BaseURL+"/cgi-bin/stable_token", bytes.NewReader(payload))
	if err != nil {
		return "", &Error{API: APIStableToken, Kind: KindUnavailable}
	}
	req.Header.Set("Content-Type", "application/json")

	var out struct {
		wxStatus
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if e := c.do(ctx, APIStableToken, req, &out); e != nil {
		return "", e
	}
	if out.ErrCode != 0 || out.AccessToken == "" {
		return "", &Error{API: APIStableToken, ErrCode: out.ErrCode, Kind: KindUnavailable}
	}

	// 有效期不比安全余量长就不缓存，下次再取。
	if ttl := time.Duration(out.ExpiresIn)*time.Second - accessTokenSafetyMargin; ttl > 0 {
		if err = c.rdb.Set(ctx, c.accessTokenKey(), out.AccessToken, ttl).Err(); err != nil {
			return "", &Error{API: APIStableToken, Kind: KindUnavailable}
		}
	}
	return out.AccessToken, nil
}
