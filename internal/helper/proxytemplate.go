package helper

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/kingsunb/NovaVeil/internal/model"
)

// accountAliasHexLen 生成别名的长度: 8 位 hex。
const accountAliasHexLen = 8

// errProxyTemplateInvalid 代理解析失败的固定哨兵, 不透出模板原文或底层 URL 错误,
// 防止 userinfo 中的凭据泄漏到管理端响应。
var errProxyTemplateInvalid = errors.New("proxy template invalid")

// ResolveProxyTemplate 解析渠道专属代理模板, 把 userinfo 用户名中的 {account} 占位符
// 替换为指定 Key 的生效别名后返回重建的 URL。
// 必须先 url.Parse 再定位 User 信息并用 url.User/UserPassword 重建:
// 序列化时由 net/url 对 userinfo 做转义, 别名中的 @ : / 等特殊字符不会破坏 URL 结构,
// 禁止任何形式的裸字符串拼接。密码段不参与替换; 模板不含占位符或无 userinfo 时原样返回解析结果。
func ResolveProxyTemplate(template, account string) (*url.URL, error) {
	trimmed := strings.TrimSpace(template)
	prepared := strings.ReplaceAll(trimmed, model.AccountPlaceholder, model.AccountPlaceholderEscape)
	parsed, err := url.Parse(prepared)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy template: %w", err)
	}
	// 占位符只按 userinfo 语义处理: 无 userinfo 或用户名不含占位符时原样返回。
	if parsed.User == nil || !strings.Contains(parsed.User.Username(), model.AccountPlaceholder) {
		return parsed, nil
	}
	username := strings.ReplaceAll(parsed.User.Username(), model.AccountPlaceholder, account)
	password, hasPassword := parsed.User.Password()
	if hasPassword {
		parsed.User = url.UserPassword(username, password)
	} else {
		parsed.User = url.User(username)
	}
	return parsed, nil
}

// AccountAliasFor 返回渠道某把 Key 填充代理模板 {account} 的生成别名:
// 由「渠道 ID + Key ID」做 sha256 后取前 8 位 hex, 不随 Key 保存。
// 同一渠道同一 Key 恒定一致(跨请求、跨进程重启), 日志与代理服务商侧可按 Key 追踪;
// 不同 Key、不同渠道互不相同——即使多处复用同一段密钥也会得到不同别名;
// 派生自服务端 ID 而非密钥明文, 不可反推, 也无需持久化。
// 旧式单 Key 渠道传空 keyID, 得到渠道级恒定别名。
func AccountAliasFor(channelID int, keyID string) string {
	digest := sha256.Sum256([]byte(strconv.Itoa(channelID) + ":" + keyID))
	return hex.EncodeToString(digest[:])[:accountAliasHexLen]
}

// ResolveChannelProxyTemplate 用确定性派生别名解析渠道代理模板中的 {account} 占位符,
// 供模型列表获取等不经过 relay 选 Key 的辅助路径在创建 HTTP 客户端前调用:
// 含 { 的原始模板无法通过 url.Parse 的 userinfo 校验, 直接透传必然失败。
// 生效 Key 的选取与 model.Channel.PrimaryKey 一致: 首把 Key 明文非空时取 Keys[0],
// 否则视为旧式单 Key(空 keyID), 与转发路径取同一别名, 预览与实际转发行为一致。
// WARP 渠道置空代理交给转发路径。
// 渠道未配置代理模板或模板不含占位符时原样返回 nil。
func ResolveChannelProxyTemplate(channel *model.Channel) error {
	if channel.ChannelProxy != nil && strings.Contains(*channel.ChannelProxy, model.WarpProxyMarker) {
		// WARP 槽位不派生别名。
		channel.ChannelProxy = nil
		return nil
	}
	if channel.ChannelProxy == nil || !strings.Contains(*channel.ChannelProxy, model.AccountPlaceholder) {
		return nil
	}
	var keyID string
	if len(channel.Keys) > 0 && channel.Keys[0].Key != "" {
		keyID = channel.Keys[0].ID
	}
	resolved, err := ResolveProxyTemplate(*channel.ChannelProxy, AccountAliasFor(channel.ID, keyID))
	if err != nil {
		return errProxyTemplateInvalid
	}
	resolvedAddr := resolved.String()
	channel.ChannelProxy = &resolvedAddr
	return nil
}
