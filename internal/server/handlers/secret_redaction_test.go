package handlers

import (
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
)

func TestAPIKeySummaryRedactsSecret(t *testing.T) {
	secret := "sk-secret-value-1234"
	summary := apiKeyToSummary(model.APIKey{ID: 7, Name: "test", APIKey: secret, Enabled: true})
	if summary.APIKeyMasked != "****1234" {
		t.Fatalf("masked key = %q, want ****1234", summary.APIKeyMasked)
	}
	if summary.Name != "test" || summary.ID != 7 || !summary.Enabled {
		t.Fatalf("summary metadata changed: %+v", summary)
	}
}

func TestChannelAdminSummaryRedactsSecrets(t *testing.T) {
	channel := model.Channel{
		Key: "legacy-secret-5678",
		Keys: []model.ChannelKey{
			{ID: "id-a", Key: "multi-secret-1111", Remark: "first"},
			{ID: "id-b", Key: "tiny"},
		},
	}
	summary := channelAdminSummary(channel)
	if summary.Key != "" || summary.KeyMasked != "****5678" {
		t.Fatalf("legacy secret leaked or mask wrong: key=%q mask=%q", summary.Key, summary.KeyMasked)
	}
	if len(summary.Keys) != 2 {
		t.Fatalf("keys length = %d, want 2", len(summary.Keys))
	}
	if summary.Keys[0].Key != "" || summary.Keys[0].KeyMasked != "****1111" {
		t.Fatalf("multi secret leaked or mask wrong: %+v", summary.Keys[0])
	}
	if summary.Keys[1].Key != "" || summary.Keys[1].KeyMasked != "****" {
		t.Fatalf("short secret leaked or mask wrong: %+v", summary.Keys[1])
	}
	if summary.Keys[0].Remark != "first" {
		t.Fatalf("non-secret metadata changed: %+v", summary.Keys[0])
	}
	if channel.Key == "" || channel.Keys[0].Key == "" {
		t.Fatal("redaction mutated the source channel")
	}
}

func TestChannelAdminSummaryKeepsProxyPlaintext(t *testing.T) {
	proxy := "http://alice:s3cret-pass@127.0.0.1:7890"
	channel := model.Channel{Name: "proxied", ChannelProxy: &proxy}
	summary := channelAdminSummary(channel)
	if summary.ChannelProxy == nil || *summary.ChannelProxy != proxy {
		t.Fatalf("list proxy = %v, want plaintext %q", summary.ChannelProxy, proxy)
	}
	if channel.ChannelProxy == nil || *channel.ChannelProxy != proxy {
		t.Fatal("summary mutated the source proxy")
	}
}
