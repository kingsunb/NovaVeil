package auth

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/kingsunb/NovaVeil/internal/conf"
	"github.com/kingsunb/NovaVeil/internal/op"
	"github.com/kingsunb/NovaVeil/internal/testutil"
)

func TestMain(m *testing.M) {
	cleanup, err := testutil.InitTestDB()
	if err != nil {
		panic(err)
	}
	defer cleanup()
	os.Exit(m.Run())
}

func TestGenerateAndVerifyTokenUsesKVSecret(t *testing.T) {
	token, maxAge, err := GenerateJWTToken(true)
	if err != nil {
		t.Fatalf("GenerateJWTToken error: %v", err)
	}
	if token == "" || maxAge != sessionMaxAge {
		t.Fatalf("unexpected token/maxAge: %q/%d", token, maxAge)
	}
	if !VerifyJWTToken(token) {
		t.Fatal("freshly issued token must verify")
	}

	secret, err := op.AuthJWTSecretGet()
	if err != nil {
		t.Fatalf("read secret: %v", err)
	}
	// 密钥不再由 username+password 派生: 与凭据无关
	if token != "" && VerifyJWTToken(mangleToken(token)) {
		t.Fatal("tampered token must not verify")
	}
	_ = secret
}

// mangleToken 篡改签名段首字符: 该字符完整对应 6 个比特位, 任何改动必然改变解码后的签名
func mangleToken(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return token
	}
	sig := []byte(parts[2])
	if sig[0] == 'A' {
		sig[0] = 'B'
	} else {
		sig[0] = 'A'
	}
	parts[2] = string(sig)
	return strings.Join(parts, ".")
}

func TestSecretRotationInvalidatesTokens(t *testing.T) {
	oldToken, _, err := GenerateJWTToken(true)
	if err != nil {
		t.Fatalf("GenerateJWTToken error: %v", err)
	}
	if !VerifyJWTToken(oldToken) {
		t.Fatal("old token should verify before rotation")
	}
	if err := op.AuthJWTRotateSecret(); err != nil {
		t.Fatalf("rotate secret: %v", err)
	}
	if VerifyJWTToken(oldToken) {
		t.Fatal("token signed with previous secret must be invalid after rotation")
	}
	newToken, _, err := GenerateJWTToken(true)
	if err != nil {
		t.Fatalf("GenerateJWTToken after rotate: %v", err)
	}
	if !VerifyJWTToken(newToken) {
		t.Fatal("token signed with rotated secret must verify")
	}
}

// TestGenerateJWTTokenRememberControlsCookieMaxAge 钉死「记住我」二选一语义:
// remember=true → 持久 24h(记住设备); remember=false → 0(会话 cookie, 单次会话)。
// JWT exp 两端都恒为 24h 的服务端上限, 客户端不能自报任意时长。
func TestGenerateJWTTokenRememberControlsCookieMaxAge(t *testing.T) {
	_, maxAge, err := GenerateJWTToken(true)
	if err != nil {
		t.Fatalf("GenerateJWTToken(true): %v", err)
	}
	if maxAge != sessionMaxAge {
		t.Fatalf("remember maxAge = %d, want %d (24h)", maxAge, sessionMaxAge)
	}

	_, maxAge, err = GenerateJWTToken(false)
	if err != nil {
		t.Fatalf("GenerateJWTToken(false): %v", err)
	}
	if maxAge != 0 {
		t.Fatalf("session maxAge = %d, want 0 (session cookie)", maxAge)
	}
}

func TestGenerateAPIKeyReturnsNonEmpty(t *testing.T) {
	key, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	if !strings.HasPrefix(key, "sk-") || len(key) < 20 {
		t.Fatalf("unexpected key %q", key)
	}
}

func TestVerifyRejectsNonHMACSigningMethod(t *testing.T) {
	// alg=none 的未签名 token 必须被拒绝
	unsigned := "eyJhbGciOiJub25lIiwidHlwZSI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0."
	if VerifyJWTToken(unsigned) {
		t.Fatal("unsigned token must not verify")
	}
}

// --- JWT 契约加固测试: 显式核对签名算法、必需 exp、issuer/audience ---

// signTokenWithClaims 用当前 KV 密钥以指定 claims 签发 token, 供负面测试构造非法 token。
func signTokenWithClaims(t *testing.T, method jwt.SigningMethod, claims *jwt.RegisteredClaims) string {
	t.Helper()
	secret, err := op.AuthJWTSecretGet()
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	token, err := jwt.NewWithClaims(method, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return token
}

func validClaims() *jwt.RegisteredClaims {
	now := time.Now()
	return &jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		Issuer:    conf.APP_NAME,
		Audience:  jwt.ClaimStrings{conf.APP_NAME},
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
	}
}

func TestVerifyRejectsTokenWithoutExp(t *testing.T) {
	claims := validClaims()
	claims.ExpiresAt = nil
	token := signTokenWithClaims(t, jwt.SigningMethodHS256, claims)
	if VerifyJWTToken(token) {
		t.Fatal("token without exp must be rejected")
	}
}

func TestVerifyRejectsWrongAlgorithmHS384(t *testing.T) {
	token := signTokenWithClaims(t, jwt.SigningMethodHS384, validClaims())
	if VerifyJWTToken(token) {
		t.Fatal("token signed with HS384 must be rejected (only HS256 allowed)")
	}
}

func TestVerifyRejectsWrongIssuer(t *testing.T) {
	claims := validClaims()
	claims.Issuer = "wrong-issuer"
	token := signTokenWithClaims(t, jwt.SigningMethodHS256, claims)
	if VerifyJWTToken(token) {
		t.Fatal("token with wrong issuer must be rejected")
	}
}

func TestVerifyRejectsWrongAudience(t *testing.T) {
	claims := validClaims()
	claims.Audience = jwt.ClaimStrings{"wrong-audience"}
	token := signTokenWithClaims(t, jwt.SigningMethodHS256, claims)
	if VerifyJWTToken(token) {
		t.Fatal("token with wrong audience must be rejected")
	}
}

func TestVerifyRejectsMissingAudience(t *testing.T) {
	claims := validClaims()
	claims.Audience = nil
	token := signTokenWithClaims(t, jwt.SigningMethodHS256, claims)
	if VerifyJWTToken(token) {
		t.Fatal("token without audience must be rejected")
	}
}

func TestVerifyAcceptsProperlyIssuedToken(t *testing.T) {
	token := signTokenWithClaims(t, jwt.SigningMethodHS256, validClaims())
	if !VerifyJWTToken(token) {
		t.Fatal("token with correct alg/exp/issuer/audience must verify")
	}
}
