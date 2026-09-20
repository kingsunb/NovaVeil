package conf

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/spf13/viper"
)

type Server struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
	// TrustedProxies 是可信反向代理的 CIDR/IP 列表(如 ["127.0.0.1/32", "10.0.0.0/8"])。
	// 为空时不信任任何代理头: ClientIP 不采信可伪造的 X-Forwarded-For,
	// 登录限速等按 IP 的防护不可被伪造头绕过(直连部署安全默认)。
	// 反代部署需显式配置, 仅来自这些地址的 XFF 才被采信, TLS 反代后 ClientIP 仍为真实客户端 IP。
	// 也可通过环境变量 NOVAVEIL_SERVER_TRUSTED_PROXIES 设置(逗号分隔)。
	TrustedProxies []string `mapstructure:"trusted_proxies"`
}

type Log struct {
	Level string `mapstructure:"level"`
}

type Database struct {
	Type string `mapstructure:"type"`
	Path string `mapstructure:"path"`
}

type Security struct {
	// CookieSecure 控制 auth cookie 是否携带 Secure 属性(仅 HTTPS 下发送), 反代终止 TLS 时应开启
	CookieSecure bool `mapstructure:"cookie_secure"`
	// EncryptionKey 可选静态加密主密钥(任意字符串会派生为 32 字节 AES-256 密钥)。
	// 为空时启动器在数据目录生成 novaveil-encryption.key 并加载, 保证重启后仍可解密库内密文。
	EncryptionKey string `mapstructure:"encryption_key"`
	// AdminAPIRateLimitEnabled 为 true 时对 /api/v1 已认证非登录接口启用内存令牌桶限速,
	// 按 客户端 IP+路由 维度计数。默认关闭, 现有部署行为不变(审计 OLD-26)。
	AdminAPIRateLimitEnabled bool `mapstructure:"admin_api_rate_limit_enabled"`
	// AdminAPIRateLimitPerMinute 限速阈值, 仅在启用时生效(默认 120 请求/分钟/IP/路由)。
	AdminAPIRateLimitPerMinute float64 `mapstructure:"admin_api_rate_limit_per_minute"`
	// AdminAPIRateLimitBurst 令牌桶容量, 允许的瞬时突发请求数。
	AdminAPIRateLimitBurst int `mapstructure:"admin_api_rate_limit_burst"`
}

type Config struct {
	Server   Server   `mapstructure:"server"`
	Log      Log      `mapstructure:"log"`
	Database Database `mapstructure:"database"`
	Security Security `mapstructure:"security"`
}

var AppConfig Config

func Load(path string) error {
	if path != "" {
		viper.SetConfigFile(path)
	} else {
		viper.SetConfigName("config")
		viper.SetConfigType("json")
		viper.AddConfigPath("data")
	}

	viper.AutomaticEnv()
	viper.SetEnvPrefix(APP_NAME)
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	setDefaults()

	if err := viper.ReadInConfig(); err == nil {
		log.Infof("Using config file: %s", viper.ConfigFileUsed())
	} else {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			log.Infof("Config file not found, creating default config")
			if err := os.MkdirAll("data", 0o700); err != nil {
				log.Errorf("Failed to create data directory: %v", err)
			}
			if err := viper.SafeWriteConfigAs("data/config.json"); err != nil {
				log.Errorf("Failed to create default config: %v", err)
			} else {
				_ = os.Chmod("data/config.json", 0o600)
			}
		} else {
			return fmt.Errorf("error reading config file: %w", err)
		}
	}

	if err := viper.Unmarshal(&AppConfig); err != nil {
		return fmt.Errorf("unable to decode config into struct: %w", err)
	}
	applyEnvOverrides()
	// 审计 OLD-25: 反序列化来自配置文件/环境变量的 host/port/path 等关键项在启动早期
	// 显式校验, 坏配置在 Init/Start 之前快速失败, 而不是运行到一半以不可读错误炸掉。
	if err := AppConfig.Validate(); err != nil {
		return fmt.Errorf("无效配置: %w", err)
	}
	return nil
}

// Validate 校验经 unmarshal + env overrides 后的关键配置项(审计 OLD-25)。
// host 必须非空; port 必须在 1..65535; database.path 必须非空且不能包含 NUL。
func (c Config) Validate() error {
	if strings.TrimSpace(c.Server.Host) == "" {
		return fmt.Errorf("server.host 不能为空")
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port 必须在 1-65535 之间")
	}
	if strings.TrimSpace(c.Database.Path) == "" {
		return fmt.Errorf("database.path 不能为空")
	}
	if strings.ContainsRune(c.Database.Path, 0) {
		return fmt.Errorf("database.path 包含非法 NUL 字节")
	}
	if c.Security.AdminAPIRateLimitEnabled {
		if c.Security.AdminAPIRateLimitPerMinute <= 0 {
			return fmt.Errorf("security.admin_api_rate_limit_per_minute 必须大于 0")
		}
		if c.Security.AdminAPIRateLimitBurst < 1 {
			return fmt.Errorf("security.admin_api_rate_limit_burst 至少为 1")
		}
	}
	return nil
}

// applyEnvOverrides 处理 viper 无法自动从环境变量解析为切片的配置项。
// NOVAVEIL_SERVER_TRUSTED_PROXIES 以逗号分隔时覆盖配置文件中的 trusted_proxies。
func applyEnvOverrides() {
	envKey := strings.ToUpper(APP_NAME) + "_SERVER_TRUSTED_PROXIES"
	if raw := os.Getenv(envKey); raw != "" {
		parts := strings.Split(raw, ",")
		result := make([]string, 0, len(parts))
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				result = append(result, t)
			}
		}
		if len(result) > 0 {
			AppConfig.Server.TrustedProxies = result
		}
	}
}

func setDefaults() {
	viper.SetDefault("server.host", "127.0.0.1")
	viper.SetDefault("server.port", 8080)
	viper.SetDefault("database.type", "sqlite")
	viper.SetDefault("database.path", "data/data.db")
	viper.SetDefault("log.level", "info")
	viper.SetDefault("security.cookie_secure", false)
	viper.SetDefault("security.encryption_key", "")
	viper.SetDefault("security.admin_api_rate_limit_enabled", false)
	viper.SetDefault("security.admin_api_rate_limit_per_minute", 120)
	viper.SetDefault("security.admin_api_rate_limit_burst", 30)
}
