package op

import (
	"fmt"

	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/seal"
)

// sealChannelForDB 返回一个敏感字段已加密的渠道副本, 原渠道保持明文供缓存使用。
func sealChannelForDB(channel model.Channel) (model.Channel, error) {
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
	return dbChannel, nil
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
	return nil
}
