package op

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/seal"
)

func TestCustomHeaderSealedAtRestAndPlainInCache(t *testing.T) {
	ctx := context.Background()
	secret := "Bearer header-secret-at-rest"
	channel := model.Channel{
		Name:    "seal-header-channel",
		Type:    model.ChannelProviderOpenAI,
		Enabled: true,
		BaseURL: "https://example.invalid",
		CustomHeader: []model.CustomHeader{
			{HeaderKey: "Authorization", HeaderValue: secret},
			{HeaderKey: "Accept", HeaderValue: ""},
		},
	}
	if err := ChannelCreate(&channel, ctx); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		db.GetDB().Delete(&model.Channel{}, channel.ID)
		channelCache.Del(channel.ID)
	})

	var stored model.Channel
	if err := db.GetDB().First(&stored, channel.ID).Error; err != nil {
		t.Fatalf("load stored: %v", err)
	}
	if len(stored.CustomHeader) != 2 {
		t.Fatalf("headers = %+v", stored.CustomHeader)
	}
	if stored.CustomHeader[0].HeaderKey != "Authorization" {
		t.Fatalf("header key changed: %+v", stored.CustomHeader[0])
	}
	if !seal.IsSealed(stored.CustomHeader[0].HeaderValue) {
		t.Fatalf("header value must be sealed, got %q", stored.CustomHeader[0].HeaderValue)
	}
	if strings.Contains(stored.CustomHeader[0].HeaderValue, secret) {
		t.Fatal("sealed header still contains plaintext")
	}
	if stored.CustomHeader[1].HeaderValue != "" {
		t.Fatalf("empty header value = %q, want empty", stored.CustomHeader[1].HeaderValue)
	}
	opened, err := seal.Open(stored.CustomHeader[0].HeaderValue)
	if err != nil || opened != secret {
		t.Fatalf("open header = %q, %v", opened, err)
	}

	cached, err := ChannelGet(channel.ID)
	if err != nil {
		t.Fatalf("cache get: %v", err)
	}
	if cached.CustomHeader[0].HeaderValue != secret {
		t.Fatalf("cache header = %q, want plaintext", cached.CustomHeader[0].HeaderValue)
	}
	if channel.CustomHeader[0].HeaderValue != secret {
		t.Fatal("create mutated the caller's header value")
	}
}

func TestLegacyPlainCustomHeaderStillOpens(t *testing.T) {
	channel := model.Channel{
		ID:   910021,
		Name: "legacy-plain-header",
		Type: model.ChannelProviderOpenAI,
		CustomHeader: []model.CustomHeader{
			{HeaderKey: "X-Legacy", HeaderValue: "plain-legacy-value"},
		},
	}
	t.Cleanup(func() {
		db.GetDB().Delete(&model.Channel{}, channel.ID)
		channelCache.Del(channel.ID)
	})
	if err := db.GetDB().Create(&channel).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := channelRefreshCache(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	cached, err := ChannelGet(channel.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cached.CustomHeader[0].HeaderValue != "plain-legacy-value" {
		t.Fatalf("legacy header = %q", cached.CustomHeader[0].HeaderValue)
	}
}

func TestSecretSettingsSealedAtRest(t *testing.T) {
	ctx := context.Background()
	type row struct {
		key   model.SettingKey
		value string
	}
	cases := []row{
		{model.SettingKeyProxyURL, "http://user:s3cret@10.1.2.3:8888"},
		{model.SettingKeyProxyPool, `[{"id":"p1","name":"lan","url":"http://user:pool-secret@192.168.1.9:1080","enabled":true}]`},
		{model.SettingKeyHeaderTemplates, `[{"name":"probe","headers":[{"header_key":"X-Token","header_value":"template-secret-value"}]}]`},
	}
	original := map[model.SettingKey]string{}
	for _, tc := range cases {
		if cur, err := SettingGetString(tc.key); err == nil {
			original[tc.key] = cur
		}
	}
	t.Cleanup(func() {
		for _, tc := range cases {
			settingCache.Set(tc.key, "force-rewrite")
			_ = SettingSetString(tc.key, original[tc.key])
		}
		_ = settingRefreshCache(ctx)
	})

	for _, tc := range cases {
		if err := SettingSetString(tc.key, tc.value); err != nil {
			t.Fatalf("set %s: %v", tc.key, err)
		}
		got, err := SettingGetString(tc.key)
		if err != nil || got != tc.value {
			t.Fatalf("cache %s = %q, %v", tc.key, got, err)
		}
		var stored model.Setting
		if err := db.GetDB().Where("key = ?", tc.key).First(&stored).Error; err != nil {
			t.Fatalf("load %s: %v", tc.key, err)
		}
		if stored.Value == tc.value || !seal.IsSealed(stored.Value) {
			t.Fatalf("%s must be sealed at rest, got %q", tc.key, stored.Value)
		}
		if strings.Contains(stored.Value, "s3cret") || strings.Contains(stored.Value, "pool-secret") || strings.Contains(stored.Value, "template-secret-value") {
			t.Fatalf("%s ciphertext leaked a secret: %s", tc.key, stored.Value)
		}
		plain, err := seal.Open(stored.Value)
		if err != nil || plain != tc.value {
			t.Fatalf("open %s = %q, %v", tc.key, plain, err)
		}
	}
}

func TestLegacyPlainSettingStillOpens(t *testing.T) {
	const legacy = "http://127.0.0.1:9"
	original, _ := SettingGetString(model.SettingKeyProxyURL)
	t.Cleanup(func() {
		settingCache.Set(model.SettingKeyProxyURL, "force-rewrite")
		_ = SettingSetString(model.SettingKeyProxyURL, original)
	})
	if err := db.GetDB().Model(&model.Setting{}).Where("key = ?", model.SettingKeyProxyURL).Update("value", legacy).Error; err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	settingCache.Del(model.SettingKeyProxyURL)
	got, err := SettingGetString(model.SettingKeyProxyURL)
	if err != nil || got != legacy {
		t.Fatalf("legacy proxy_url = %q, %v", got, err)
	}
}

func TestJWTSecretSealedAtRest(t *testing.T) {
	secret, err := AuthJWTSecretGet()
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	var stored model.Setting
	if err := db.GetDB().Where("key = ?", model.SettingKeyAuthJWTSecret).First(&stored).Error; err != nil {
		t.Fatalf("load jwt: %v", err)
	}
	if !seal.IsSealed(stored.Value) || strings.Contains(stored.Value, secret) {
		t.Fatalf("jwt must be sealed, got %q", stored.Value)
	}
	plain, err := seal.Open(stored.Value)
	if err != nil || plain != secret {
		t.Fatalf("open jwt = %q, %v", plain, err)
	}
	cached, err := settingGetInternal(model.SettingKeyAuthJWTSecret)
	if err != nil || cached != secret {
		t.Fatalf("cache jwt = %q, %v", cached, err)
	}
}

func TestDBExportMasksHeadersAndOmitsProxySettings(t *testing.T) {
	ctx := context.Background()
	const (
		channelID    = 910031
		headerSecret = "Bearer export-header-secret"
		proxySecret  = "http://export-user:export-pass@10.9.8.7:3128"
	)
	originalProxy, _ := SettingGetString(model.SettingKeyProxyURL)
	originalPool, _ := SettingGetString(model.SettingKeyProxyPool)
	originalTemplates, _ := SettingGetString(model.SettingKeyHeaderTemplates)
	t.Cleanup(func() {
		db.GetDB().Delete(&model.Channel{}, channelID)
		for key, value := range map[model.SettingKey]string{
			model.SettingKeyProxyURL:        originalProxy,
			model.SettingKeyProxyPool:       originalPool,
			model.SettingKeyHeaderTemplates: originalTemplates,
		} {
			settingCache.Set(key, "force-rewrite")
			_ = SettingSetString(key, value)
		}
	})

	proxy := proxySecret
	channel := model.Channel{
		ID:           channelID,
		Name:         "export-mask-headers",
		Type:         model.ChannelProviderOpenAI,
		Enabled:      true,
		BaseURL:      "https://example.invalid",
		Key:          "sk-export-channel-key",
		ChannelProxy: &proxy,
		Keys:         []model.ChannelKey{{ID: "k1", Key: "sk-export-multi-key"}},
		CustomHeader: []model.CustomHeader{
			{HeaderKey: "X-Api-Token", HeaderValue: headerSecret},
			{HeaderKey: "Accept", HeaderValue: ""},
		},
	}
	if _, err := sealChannelForDB(channel); err != nil {
		t.Fatalf("preflight seal: %v", err)
	}
	stored, err := sealChannelForDB(channel)
	if err != nil {
		t.Fatalf("seal channel: %v", err)
	}
	stored.ID = channelID
	if err := db.GetDB().Create(&stored).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if err := SettingSetString(model.SettingKeyProxyURL, "http://user:proxy-url-secret@127.0.0.1:7890"); err != nil {
		t.Fatalf("set proxy_url: %v", err)
	}
	if err := SettingSetString(model.SettingKeyProxyPool, `[{"id":"px","name":"box","url":"socks5://pool-user:pool-pass@10.0.0.8:1080","enabled":true}]`); err != nil {
		t.Fatalf("set proxy_pool: %v", err)
	}
	if err := SettingSetString(model.SettingKeyHeaderTemplates, `[{"name":"codex-export","headers":[{"header_key":"X-Token","header_value":"template-export-secret"},{"header_key":"Accept","header_value":""}]}]`+"\n"); err != nil {
		t.Fatalf("set templates: %v", err)
	}

	dump, err := DBExportAll(ctx)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	payload, err := json.Marshal(dump)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	exported := string(payload)
	for _, secret := range []string{
		headerSecret, proxySecret, "proxy-url-secret", "pool-pass", "template-export-secret", "nv1:",
	} {
		if strings.Contains(exported, secret) {
			t.Fatalf("export leaked %q", secret)
		}
	}
	for _, key := range []string{"sk-export-channel-key", "sk-export-multi-key"} {
		if !strings.Contains(exported, key) {
			t.Fatalf("export missing plaintext key %q", key)
		}
	}
	for _, key := range []string{`"proxy_url"`, `"proxy_pool"`, `"auth_jwt_secret"`} {
		if strings.Contains(exported, key) {
			t.Fatalf("export must omit %s", key)
		}
	}
	var found bool
	for _, ch := range dump.Channels {
		if ch.ID != channelID {
			continue
		}
		found = true
		if ch.Key != "sk-export-channel-key" || ch.Keys[0].Key != "sk-export-multi-key" {
			t.Fatalf("keys = %q / %+v", ch.Key, ch.Keys)
		}
		if ch.ChannelProxy == nil || *ch.ChannelProxy != "****" {
			t.Fatalf("proxy = %v", ch.ChannelProxy)
		}
		if len(ch.CustomHeader) != 2 || ch.CustomHeader[0].HeaderKey != "X-Api-Token" || ch.CustomHeader[0].HeaderValue != "****" {
			t.Fatalf("custom header = %+v", ch.CustomHeader)
		}
		if ch.CustomHeader[1].HeaderValue != "" {
			t.Fatalf("empty header masked: %+v", ch.CustomHeader[1])
		}
	}
	if !found {
		t.Fatal("exported channel missing")
	}
	var templates string
	for _, s := range dump.Settings {
		if s.Key == model.SettingKeyHeaderTemplates {
			templates = s.Value
		}
	}
	if !strings.Contains(templates, `"codex-export"`) || !strings.Contains(templates, `"X-Token"`) {
		t.Fatalf("template structure missing: %s", templates)
	}
	if strings.Contains(templates, "template-export-secret") {
		t.Fatalf("template secret leaked: %s", templates)
	}
	var parsed []model.HeaderTemplate
	if err := json.Unmarshal([]byte(templates), &parsed); err != nil {
		t.Fatalf("template json: %v", err)
	}
	if parsed[0].Headers[0].HeaderValue != "****" || parsed[0].Headers[1].HeaderValue != "" {
		t.Fatalf("template values = %+v", parsed[0].Headers)
	}
}

func TestChannelCreateAllowsPrivateChannelProxy(t *testing.T) {
	// 上游 BaseURL 已不做地址范围限制: 环回/私网 BaseURL 同样可被接受。
	if err := ValidateChannelEgressBaseURL("http://127.0.0.1:7890"); err != nil {
		t.Fatalf("BaseURL loopback should be accepted: %v", err)
	}
	if err := ValidateChannelEgressBaseURL("http://10.1.0.5:8080"); err != nil {
		t.Fatalf("BaseURL private address should be accepted: %v", err)
	}
	ctx := context.Background()
	proxy := "http://alice:s3cret@127.0.0.1:7890"
	channel := model.Channel{
		Name:         "private-proxy-channel",
		Type:         model.ChannelProviderOpenAI,
		Enabled:      true,
		BaseURL:      "https://example.invalid",
		ChannelProxy: &proxy,
	}
	if err := ChannelCreate(&channel, ctx); err != nil {
		t.Fatalf("private proxy must be accepted: %v", err)
	}
	t.Cleanup(func() {
		db.GetDB().Delete(&model.Channel{}, channel.ID)
		channelCache.Del(channel.ID)
	})
	cached, err := ChannelGet(channel.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cached.ChannelProxy == nil || *cached.ChannelProxy != proxy {
		t.Fatalf("cache proxy = %v", cached.ChannelProxy)
	}
	var stored model.Channel
	if err := db.GetDB().First(&stored, channel.ID).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if stored.ChannelProxy == nil || !seal.IsSealed(*stored.ChannelProxy) {
		t.Fatalf("proxy at rest = %v", stored.ChannelProxy)
	}
}

func TestChannelUpdateWritesProxyPlaintext(t *testing.T) {
	ctx := context.Background()
	proxy := "http://bob:keep-me@192.168.8.8:1080"
	channel := model.Channel{
		Name:         "proxy-plain-update",
		Type:         model.ChannelProviderOpenAI,
		Enabled:      true,
		BaseURL:      "https://example.invalid",
		ChannelProxy: &proxy,
	}
	if err := ChannelCreate(&channel, ctx); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		db.GetDB().Delete(&model.Channel{}, channel.ID)
		channelCache.Del(channel.ID)
	})
	name := channel.Name
	next := "http://carol:new-pass@10.9.9.9:3128"
	updated, err := ChannelUpdate(&model.ChannelUpdateRequest{
		ID:           channel.ID,
		Name:         &name,
		ChannelProxy: &next,
	}, ctx)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.ChannelProxy == nil || *updated.ChannelProxy != next {
		t.Fatalf("proxy = %v, want %q", updated.ChannelProxy, next)
	}
}
