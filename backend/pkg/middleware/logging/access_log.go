package logging

import (
	"context"
	"fmt"
	"time"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/transport"
	"github.com/go-kratos/kratos/v2/transport/http/status"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"go-wind-admin/pkg/phone"
)

// kratosRedacter 是 Kratos logging.Server 认的接口：Redact 必须返回脱敏后的字符串。
// 本仓 protoc-gen-redact 生成的是原地清空、没有返回值的 Redact()，对不上这个接口，
// 所以不能直接用 Kratos 那份中间件。
type kratosRedacter interface {
	Redact() string
}

// inPlaceRedacter 是本仓生成代码实现的脱敏：原地清空字段，不返回字符串。
type inPlaceRedacter interface {
	Redact()
}

// sensitiveProtoFieldNames 是字段名兜底名单。
// 注解会生成原地 Redact()，但以后新增口令字段的人可能忘了写 (redact.value)。
// 名单按 proto 字段名（不是 json_name）匹配 string 字段，避免这种遗漏把口令打进访问日志。
var sensitiveProtoFieldNames = map[protoreflect.Name]struct{}{
	"password":       {},
	"old_password":   {},
	"new_password":   {},
	"credential":     {},
	"old_credential": {},
	"new_credential": {},
	"client_secret":  {},
	"refresh_token":  {},
	"access_token":   {},
	"secret":         {},
	"token":          {},
	// 微信 getPhoneNumber 的 code。wx.login 的 code 字段就叫 code，靠注解脱敏：
	// 角色编码、字典编码等业务字段也叫 code，不能放进这份名单。
	"phone_code": {},
}

// contactRedaction 联系方式在日志副本里的处理方式。
type contactRedaction int

const (
	redactClear contactRedaction = iota // 清空
	redactPhone                         // 手机号打码：138****0000
	redactEmail                         // 邮箱打码：a***@example.com
)

// contactFieldRedactions 按消息全名登记的 string 字段处理：手机号和邮箱在访问日志里只留打码后的样子。
// 手机号是登录凭证，改号、发码、管理员改用户资料的请求都带着它。
// 这些字段名（phone、email、mobile、code）太常见，不能放进上面按字段名匹配的兜底名单。
var contactFieldRedactions = map[protoreflect.FullName]map[protoreflect.Name]contactRedaction{
	"identity.service.v1.BindPhoneRequest":  {"phone": redactPhone, "code": redactClear},
	"identity.service.v1.BindEmailRequest":  {"email": redactEmail, "verification_code": redactClear},
	"identity.service.v1.PhoneVerification": {"phone": redactPhone},
	"identity.service.v1.EmailVerification": {"email": redactEmail},
	"identity.service.v1.User":              {"mobile": redactPhone, "email": redactEmail},
}

func redactContactValue(kind contactRedaction, value string) string {
	switch kind {
	case redactPhone:
		return phone.Mask(value)
	case redactEmail:
		return phone.MaskEmail(value)
	default:
		return ""
	}
}

// AccessLogServer 是访问日志中间件，替换 Kratos logging.Server。
// 字段名、级别、latency、code、reason 与 Kratos 原版一致。
// 记录参数时不改原始 req：handler 还要用里面的口令。
func AccessLogServer(logger log.Logger) middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (reply any, err error) {
			var (
				code      int32
				reason    string
				kind      string
				operation string
			)

			code = int32(status.FromGRPCCode(codes.OK))

			startTime := time.Now()
			if info, ok := transport.FromServerContext(ctx); ok {
				kind = info.Kind().String()
				operation = info.Operation()
			}
			reply, err = handler(ctx, req)
			if se := errors.FromError(err); se != nil {
				code = se.Code
				reason = se.Reason
			}
			level, stack := extractAccessLogError(err)
			kvs := []any{
				"kind", "server",
				"component", kind,
				"operation", operation,
				"args", FormatArgs(req),
				"code", code,
				"reason", reason,
				"stack", stack,
				"latency", time.Since(startTime).Seconds(),
			}
			// kratos log.WithContext 不会把 ctx 交给底层 Logger，trace_id 要在这里显式带上；
			// 没有有效 span（未配置 tracer exporter）时不多输出字段。
			if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
				kvs = append(kvs, "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
			}
			log.NewHelper(log.WithContext(ctx, logger)).Log(level, kvs...)
			return
		}
	}
}

// FormatArgs 把请求格式化成可写入日志的字符串。
// proto 消息先克隆，在克隆上做原地 Redact 和字段名兜底，再输出克隆。
// 其它类型保持 Kratos extractArgs 的顺序：Redact() string，其次 String()，最后 %+v。
func FormatArgs(req any) string {
	if msg, ok := req.(proto.Message); ok && msg != nil {
		cloned := proto.Clone(msg)
		if cloned != nil && cloned.ProtoReflect().IsValid() {
			if redacter, ok := cloned.(inPlaceRedacter); ok {
				redacter.Redact()
			}
			redactSensitiveStrings(cloned.ProtoReflect())
			if stringer, ok := cloned.(fmt.Stringer); ok {
				return stringer.String()
			}
			return fmt.Sprintf("%+v", cloned)
		}
	}
	if redacter, ok := req.(kratosRedacter); ok {
		return redacter.Redact()
	}
	if stringer, ok := req.(fmt.Stringer); ok {
		return stringer.String()
	}
	return fmt.Sprintf("%+v", req)
}

func extractAccessLogError(err error) (log.Level, string) {
	if err != nil {
		return log.LevelError, fmt.Sprintf("%+v", err)
	}
	return log.LevelInfo, ""
}

func sensitiveProtoField(name protoreflect.Name) bool {
	_, ok := sensitiveProtoFieldNames[name]
	return ok
}

// redactSensitiveStrings 递归清空敏感 string 字段，覆盖嵌套消息、repeated 和 map 的值。
// 每一层嵌套消息还会调它自己生成的 Redact()：生成的 Redact() 不会下钻 oneof 里的消息
// （例如 VerifyContactRequest.phone / .email），只靠顶层那一次调用，里面带注解的验证码会漏进日志。
func redactSensitiveStrings(m protoreflect.Message) {
	if !m.IsValid() {
		return
	}
	if redacter, ok := m.Interface().(inPlaceRedacter); ok {
		redacter.Redact()
	}
	contactRules := contactFieldRedactions[m.Descriptor().FullName()]
	var clear []protoreflect.FieldDescriptor
	masked := map[protoreflect.FieldDescriptor]string{}
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if kind, ok := contactRules[fd.Name()]; ok && fd.Kind() == protoreflect.StringKind && !fd.IsList() && !fd.IsMap() {
			if kind == redactClear {
				clear = append(clear, fd)
			} else {
				masked[fd] = redactContactValue(kind, v.String())
			}
			return true
		}
		switch {
		case fd.IsMap():
			redactSensitiveMap(fd, v.Map())
		case fd.IsList():
			redactSensitiveList(fd, v.List())
		case fd.Message() != nil:
			redactSensitiveStrings(v.Message())
		case fd.Kind() == protoreflect.StringKind && sensitiveProtoField(fd.Name()):
			clear = append(clear, fd)
		}
		return true
	})
	for _, fd := range clear {
		m.Clear(fd)
	}
	for fd, value := range masked {
		m.Set(fd, protoreflect.ValueOfString(value))
	}
}

func redactSensitiveList(fd protoreflect.FieldDescriptor, list protoreflect.List) {
	if fd.Message() != nil {
		for i := 0; i < list.Len(); i++ {
			redactSensitiveStrings(list.Get(i).Message())
		}
		return
	}
	if fd.Kind() == protoreflect.StringKind && sensitiveProtoField(fd.Name()) {
		for i := 0; i < list.Len(); i++ {
			list.Set(i, protoreflect.ValueOfString(""))
		}
	}
}

func redactSensitiveMap(fd protoreflect.FieldDescriptor, mp protoreflect.Map) {
	valueDesc := fd.MapValue()
	if valueDesc.Message() != nil {
		mp.Range(func(_ protoreflect.MapKey, v protoreflect.Value) bool {
			redactSensitiveStrings(v.Message())
			return true
		})
		return
	}
	if valueDesc.Kind() != protoreflect.StringKind || !sensitiveProtoField(fd.Name()) {
		return
	}
	var keys []protoreflect.MapKey
	mp.Range(func(k protoreflect.MapKey, _ protoreflect.Value) bool {
		keys = append(keys, k)
		return true
	})
	for _, k := range keys {
		mp.Set(k, protoreflect.ValueOfString(""))
	}
}
