package resp

const (
	ErrBadRequest           = "请求参数无效"
	ErrInvalidJSON          = "JSON 格式错误"
	ErrInvalidParam         = "参数无效"
	ErrValidation           = "输入校验失败"
	ErrDuplicateResource    = "资源已存在"
	ErrResourceNotFound     = "资源不存在"
	ErrInternalServer       = "服务器内部错误"
	ErrDatabase             = "数据库操作失败"
	ErrUnauthorized         = "认证失败"
	ErrMustChangePassword   = "请先修改初始密码，再执行该操作"
	ErrTooManyLoginAttempts = "登录失败次数过多，请稍后再试"
	// 密钥级限速(internal/keylimit)的 429 文案, fail-fast 语义下客户端按
	// Retry-After 头退避, 文案只作人读说明。
	// #nosec G101 -- 错误文案中的 "API key" 不是硬编码凭据。
	ErrAPIKeyConcurrencyFull = "API 密钥并发已达上限"
	// #nosec G101 -- 错误文案中的 "API key" 不是硬编码凭据。
	ErrAPIKeyRateLimited = "API 密钥请求过快，已被限流"
)

// 机器可读的错误标记响应头: 特殊错误的判定走头而非 message 文案,
// 文案今后可自由调整而不静默破坏前端契约(强制改密引导曾按原文案精确匹配)。
const (
	ErrMarkerHeader                 = "X-NovaVeil-Error"
	ErrMarkerPasswordChangeRequired = "password_change_required"
)
