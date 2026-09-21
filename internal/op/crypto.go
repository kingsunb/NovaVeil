package op

import (
	"fmt"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/seal"
)

// redactedSecret 是备份导出与渠道列表使用的精确脱敏哨兵。
// 写入路径拒绝把该哨兵加密后当作真实凭据落库。
const redactedSecret = "****"

// rejectRedactedCredential 拒绝把精确脱敏哨兵当作凭据保存。
func rejectRedactedCredential(field, value string) error {
	if value == redactedSecret {
		return fmt.Errorf("%s已脱敏(****)，不能作为凭据写入", field)
	}
	return nil
}

// sealChannelForDB 返回一个敏感字段已加密的渠道副本, 原渠道保持明文供缓存使用。
func sealChannelForDB(channel model.Channel) (model.Channel, error) {
	if err := rejectRedactedCredential("渠道密钥", channel.Key); err != nil {
		return model.Channel{}, err
	}
	if channel.ChannelProxy != nil {
		if err := rejectRedactedCredential("渠道代理", *channel.ChannelProxy); err != nil {
			return model.Channel{}, err
		}
	}
	for _, key := range channel.Keys {
		if err := rejectRedactedCredential("渠道密钥", key.Key); err != nil {
			return model.Channel{}, err
		}
	}

	dbChannel := channel
	var err error
	if channel.Key != "" {
		dbChannel.Key, err = seal.Seal(channel.Key)
		if err != nil {
			return model.Channel{}, fmt.Errorf("加密渠道密钥失败: %w", err)
		}
	}
	if channel.ChannelProxy != nil && *channel.ChannelProxy != "" {
		sealed, sErr := seal.Seal(*channel.ChannelProxy)
		if sErr != nil {
			return model.Channel{}, fmt.Errorf("加密渠道代理凭据失败: %w", sErr)
		}
		dbChannel.ChannelProxy = &sealed
	}
	if len(channel.Keys) > 0 {
		dbChannel.Keys = make([]model.ChannelKey, len(channel.Keys))
		copy(dbChannel.Keys, channel.Keys)
		for i := range dbChannel.Keys {
			if dbChannel.Keys[i].Key == "" {
				continue
			}
			dbChannel.Keys[i].Key, err = seal.Seal(dbChannel.Keys[i].Key)
			if err != nil {
				return model.Channel{}, fmt.Errorf("加密渠道密钥失败: %w", err)
			}
		}
	}
	headers, err := sealCustomHeaders(channel.CustomHeader)
	if err != nil {
		return model.Channel{}, err
	}
	dbChannel.CustomHeader = headers
	return dbChannel, nil
}

// sealCustomHeaders 返回 header_value 已加密的副本。空值保持空串, 不加密。
// 调用方切片不被修改, 以便进程内缓存继续使用明文。
func sealCustomHeaders(headers []model.CustomHeader) ([]model.CustomHeader, error) {
	if headers == nil {
		return nil, nil
	}
	sealed := make([]model.CustomHeader, len(headers))
	copy(sealed, headers)
	for i := range sealed {
		if sealed[i].HeaderValue == "" {
			continue
		}
		value, err := seal.Seal(sealed[i].HeaderValue)
		if err != nil {
			return nil, fmt.Errorf("加密自定义请求头失败: %w", err)
		}
		sealed[i].HeaderValue = value
	}
	return sealed, nil
}

// openCustomHeaders 解密自定义头的值。无 nv1: 前缀的存量明文按原样保留。
func openCustomHeaders(headers []model.CustomHeader) error {
	for i := range headers {
		if headers[i].HeaderValue == "" {
			continue
		}
		plain, err := seal.Open(headers[i].HeaderValue)
		if err != nil {
			return err
		}
		headers[i].HeaderValue = plain
	}
	return nil
}

// openChannelForCache 解密从 DB 读到的渠道敏感字段; 无前缀的存量明文按原样返回。
func openChannelForCache(channel *model.Channel) error {
	if channel == nil {
		return nil
	}
	var err error
	if channel.Key != "" {
		channel.Key, err = seal.Open(channel.Key)
		if err != nil {
			return fmt.Errorf("解密渠道 %d(%s) 旧式 Key 失败: %w", channel.ID, channel.Name, err)
		}
	}
	if channel.ChannelProxy != nil && *channel.ChannelProxy != "" {
		opened, oErr := seal.Open(*channel.ChannelProxy)
		if oErr != nil {
			return fmt.Errorf("解密渠道 %d(%s) 代理凭据失败: %w", channel.ID, channel.Name, oErr)
		}
		openedVal := opened
		channel.ChannelProxy = &openedVal
	}
	for i := range channel.Keys {
		if channel.Keys[i].Key == "" {
			continue
		}
		channel.Keys[i].Key, err = seal.Open(channel.Keys[i].Key)
		if err != nil {
			return fmt.Errorf("解密渠道 %d(%s) Key 失败: %w", channel.ID, channel.Name, err)
		}
	}
	if err := openCustomHeaders(channel.CustomHeader); err != nil {
		return fmt.Errorf("解密渠道 %d(%s) 自定义请求头失败: %w", channel.ID, channel.Name, err)
	}
	return nil
}
