package auth

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/kingsunb/NovaVeil/internal/conf"
	"github.com/kingsunb/NovaVeil/internal/op"
)

// sessionMaxAge 是「记住此设备」登录会话的 cookie 有效期(秒): JWT exp 与持久 cookie MaxAge 均按此签发。
const sessionMaxAge = 24 * 3600

// GenerateJWTToken 签发一个服务端有效期固定 24 小时的 JWT, 返回 (token, cookieMaxAge秒)。
// remember=true 时 cookieMaxAge=sessionMaxAge, cookie 持久 24 小时(记住设备);
// remember=false 时 cookieMaxAge=0, 由 SetAuthCookie 落成不带 Max-Age 的会话 cookie,
// 浏览器关闭即失效(单次会话)。JWT exp 始终钳在 24h 上限而不由客户端指定——客户端只能
// 二选一, 不能自签任意时长的长期凭据(L-8 安全边界不变)。
func GenerateJWTToken(remember bool) (string, int, error) {
	secret, err := op.AuthJWTSecretGet()
	if err != nil {
		return "", 0, err
	}
	now := time.Now()
	claims := &jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		Issuer:    conf.APP_NAME,
		Audience:  jwt.ClaimStrings{conf.APP_NAME},
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(sessionMaxAge) * time.Second)),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		return "", 0, err
	}
	maxAge := 0
	if remember {
		maxAge = sessionMaxAge
	}
	return token, maxAge, nil
}

func VerifyJWTToken(token string) bool {
	secret, err := op.AuthJWTSecretGet()
	if err != nil {
		return false
	}
	jwtToken, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		// 钉死签名算法: 仅接受 HS256, 拒绝 alg=none/RS256/HS384/HS512 等。
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	},
		// 强制要求 exp 声明: 无过期的 token 可永久凭据, 仅能靠轮换密钥撤销。
		jwt.WithExpirationRequired(),
		// 校验 issuer/audience: 防止其他系统用同一密钥签发的 token 交叉使用。
		jwt.WithIssuer(conf.APP_NAME),
		jwt.WithAudience(conf.APP_NAME),
	)
	if err != nil || !jwtToken.Valid {
		return false
	}
	return true
}

func GenerateAPIKey() (string, error) {
	const keyChars = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, 48)
	maxI := big.NewInt(int64(len(keyChars)))
	for i := range b {
		n, err := rand.Int(rand.Reader, maxI)
		if err != nil {
			return "", err
		}
		b[i] = keyChars[n.Int64()]
	}
	return "sk-" + conf.APP_NAME + "-" + string(b), nil
}
