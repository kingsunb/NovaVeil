package resp

const (
	ErrBadRequest           = "Invalid request parameters"
	ErrInvalidJSON          = "Invalid JSON format"
	ErrInvalidParam         = "Invalid parameter"
	ErrValidation           = "Input validation failed"
	ErrDuplicateResource    = "Resource already exists"
	ErrResourceNotFound     = "Resource not found"
	ErrInternalServer       = "An unexpected error occurred"
	ErrDatabase             = "Database operation failed"
	ErrUnauthorized         = "Authentication failed"
	ErrMustChangePassword   = "Password change required before performing this operation"
	ErrTooManyLoginAttempts = "Too many failed login attempts, please try again later"
	// 密钥级限速(internal/keylimit)的 429 文案, fail-fast 语义下客户端按
	// Retry-After 头退避, 文案只作人读说明。
	// #nosec G101 -- 错误文案中的 "API key" 不是硬编码凭据。
	ErrAPIKeyConcurrencyFull = "API key concurrency limit reached"
	// #nosec G101 -- 错误文案中的 "API key" 不是硬编码凭据。
	ErrAPIKeyRateLimited     = "API key rate limit exceeded"
)

// 机器可读的错误标记响应头: 特殊错误的判定走头而非 message 文案,
// 文案今后可自由调整而不静默破坏前端契约(强制改密引导曾按原文案精确匹配)。
const (
	ErrMarkerHeader                 = "X-NovaVeil-Error"
	ErrMarkerPasswordChangeRequired = "password_change_required"
)
