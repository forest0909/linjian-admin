// Package wechattest 测试用的微信服务端替身：一个 httptest 服务，实现小程序登录用到的三个接口，
// 按接口分别计数。只给测试用，不要在生产代码里引用。
package wechattest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// 假服务端固定的凭据。测试用它们断言「错误和日志里没有 Secret、access_token」。
const (
	AppID       = "wx-test-appid"
	AppSecret   = "wx-test-SECRET-do-not-log"
	AccessToken = "wx-test-ACCESS-TOKEN-do-not-log"
)

// PhoneReply 一个手机号 code 对应的应答。
type PhoneReply struct {
	CountryCode     string
	PurePhoneNumber string
	// WatermarkAppID 留空表示用服务端自己的 AppID。
	WatermarkAppID string
	// OmitPhoneInfo 为 true 时应答里没有 phone_info（errcode 仍是 0）。
	OmitPhoneInfo bool
}

// Calls 各接口被调用的次数。
type Calls struct {
	Code2Session int
	StableToken  int
	GetPhone     int
}

// Server 微信服务端替身。字段在请求之间可以改，改之前不用加锁的只有创建后、发请求前那一段。
type Server struct {
	*httptest.Server

	mu        sync.Mutex
	calls     Calls
	openIDs   map[string]string     // wx.login code → openid
	usedCodes map[string]bool       // 已经换过一次的 code
	phones    map[string]PhoneReply // 手机号 code → 应答
	failWith  map[string]int        // 接口路径 → 直接返回这个 errcode
	hang      map[string]bool       // 接口路径 → 不应答，直到客户端超时
	// tokenInvalidOnce 下一次换手机号返回 40001，之后恢复。
	tokenInvalidOnce bool
	release          chan struct{}
}

// 接口路径，SetError / SetHang 的参数。
const (
	PathCode2Session = "/sns/jscode2session"
	PathStableToken  = "/cgi-bin/stable_token"
	PathGetPhone     = "/wxa/business/getuserphonenumber"
)

// New 起一个假服务端，测试结束时自动关闭。
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{
		openIDs:   map[string]string{},
		usedCodes: map[string]bool{},
		phones:    map[string]PhoneReply{},
		failWith:  map[string]int{},
		hang:      map[string]bool{},
		release:   make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc(PathCode2Session, s.code2Session)
	mux.HandleFunc(PathStableToken, s.stableToken)
	mux.HandleFunc(PathGetPhone, s.getPhone)
	s.Server = httptest.NewServer(mux)
	t.Cleanup(func() {
		close(s.release) // 放掉还挂着的请求，否则 Close 会一直等
		s.Server.Close()
	})
	return s
}

// AddCode 登记一个 wx.login code，换出来的 openid 是 openid。每个 code 只能换一次。
func (s *Server) AddCode(code, openid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.openIDs[code] = openid
}

// AddPhone 登记一个手机号 code。
func (s *Server) AddPhone(phoneCode string, reply PhoneReply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phones[phoneCode] = reply
}

// SetError 让某个接口直接返回 errcode（0 表示恢复正常）。
func (s *Server) SetError(path string, errCode int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if errCode == 0 {
		delete(s.failWith, path)
		return
	}
	s.failWith[path] = errCode
}

// SetHang 让某个接口不应答，直到客户端超时。
func (s *Server) SetHang(path string, hang bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hang[path] = hang
}

// InvalidateTokenOnce 下一次换手机号返回 40001（access_token 无效）。
func (s *Server) InvalidateTokenOnce() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenInvalidOnce = true
}

// Calls 当前各接口的调用次数。
func (s *Server) Calls() Calls {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// enter 计数并处理「直接报错」和「挂住」。返回 false 表示已经应答过了。
func (s *Server) enter(w http.ResponseWriter, r *http.Request, path string, count *int) bool {
	s.mu.Lock()
	*count++
	errCode, hang := s.failWith[path], s.hang[path]
	s.mu.Unlock()

	if hang {
		select {
		case <-r.Context().Done():
		case <-s.release:
		}
		return false
	}
	if errCode != 0 {
		// errmsg 里故意回显请求内容：适配器不能把它带出去。
		writeJSON(w, map[string]any{"errcode": errCode, "errmsg": "rejected: " + r.URL.String()})
		return false
	}
	return true
}

func (s *Server) code2Session(w http.ResponseWriter, r *http.Request) {
	if !s.enter(w, r, PathCode2Session, &s.calls.Code2Session) {
		return
	}
	q := r.URL.Query()
	if q.Get("appid") != AppID || q.Get("secret") != AppSecret {
		writeJSON(w, map[string]any{"errcode": 40125, "errmsg": "invalid appsecret"})
		return
	}
	code := q.Get("js_code")

	s.mu.Lock()
	openid, ok := s.openIDs[code]
	used := s.usedCodes[code]
	if ok {
		s.usedCodes[code] = true
	}
	s.mu.Unlock()

	switch {
	case !ok:
		writeJSON(w, map[string]any{"errcode": 40029, "errmsg": "invalid code"})
	case used:
		writeJSON(w, map[string]any{"errcode": 40163, "errmsg": "code been used"})
	default:
		writeJSON(w, map[string]any{"openid": openid, "session_key": "session-key-do-not-store"})
	}
}

func (s *Server) stableToken(w http.ResponseWriter, r *http.Request) {
	if !s.enter(w, r, PathStableToken, &s.calls.StableToken) {
		return
	}
	var body struct {
		AppID  string `json:"appid"`
		Secret string `json:"secret"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.AppID != AppID || body.Secret != AppSecret {
		writeJSON(w, map[string]any{"errcode": 40125, "errmsg": "invalid appsecret"})
		return
	}
	writeJSON(w, map[string]any{"access_token": AccessToken, "expires_in": 7200})
}

func (s *Server) getPhone(w http.ResponseWriter, r *http.Request) {
	if !s.enter(w, r, PathGetPhone, &s.calls.GetPhone) {
		return
	}
	s.mu.Lock()
	invalid := s.tokenInvalidOnce
	s.tokenInvalidOnce = false
	s.mu.Unlock()
	if invalid || r.URL.Query().Get("access_token") != AccessToken {
		writeJSON(w, map[string]any{"errcode": 40001, "errmsg": "invalid credential, access_token is invalid"})
		return
	}

	var body struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	s.mu.Lock()
	reply, ok := s.phones[body.Code]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, map[string]any{"errcode": 40029, "errmsg": "invalid code"})
		return
	}
	if reply.OmitPhoneInfo {
		writeJSON(w, map[string]any{"errcode": 0, "errmsg": "ok"})
		return
	}
	watermark := reply.WatermarkAppID
	if watermark == "" {
		watermark = AppID
	}
	writeJSON(w, map[string]any{
		"errcode": 0,
		"errmsg":  "ok",
		"phone_info": map[string]any{
			"phoneNumber":     "+" + reply.CountryCode + reply.PurePhoneNumber,
			"purePhoneNumber": reply.PurePhoneNumber,
			"countryCode":     reply.CountryCode,
			"watermark":       map[string]any{"timestamp": 1700000000, "appid": watermark},
		},
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
