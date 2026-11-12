package mask

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestoreString_PlaceholdersToOriginal(t *testing.T) {
	m := newMapping()
	ph1 := m.Recall("13800138000", "PHONE")
	ph2 := m.Recall("alice@example.com", "EMAIL")
	body := "phone " + ph1 + " email " + ph2 + " end"
	restored := RestoreString(body, m)
	assert.Equal(t, "phone 13800138000 email alice@example.com end", restored)
}

func TestRestoreString_UnresolvedLeftAsIs(t *testing.T) {
	m := newMapping()
	ph := m.Recall("13800138000", "PHONE")
	unknown := "{{PHONE_bcdfgh}}"
	body := "known " + ph + " unknown " + unknown + " end"
	restored := RestoreString(body, m)
	// 已登记占位符还原; 未登记原样保留, 绝不猜。
	assert.Equal(t, "known 13800138000 unknown "+unknown+" end", restored)
}

func TestRestoreBytes(t *testing.T) {
	m := newMapping()
	ph := m.Recall("secret-pw", "TERM")
	restored := RestoreBytes([]byte("pw="+ph), m)
	assert.Equal(t, "pw=secret-pw", string(restored))
}

func TestRestoreJSONBytes_EscapesSpecialCharacters(t *testing.T) {
	m := newMapping()
	original := "he said \"hi\"\\path\n"
	ph := m.Recall(original, "TERM")
	body := []byte(`{"content":"prefix ` + ph + ` suffix"}`)

	restored := RestoreJSONBytes(body, m)
	require.True(t, json.Valid(restored), "还原后必须是合法 JSON: %s", restored)

	var decoded struct {
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal(restored, &decoded))
	assert.Equal(t, "prefix "+original+" suffix", decoded.Content)
}

func TestRestoreJSONBytes_NoPlaceholderKeepsOriginalBytes(t *testing.T) {
	m := newMapping()
	m.Recall("13800138000", "PHONE")
	body := []byte("{ \"html\": \"<tag> & value\" }")

	restored := RestoreJSONBytes(body, m)
	assert.Equal(t, body, restored, "无占位符时不应重编码")
}

func TestRestoreJSONBytes_PlainTextFallback(t *testing.T) {
	m := newMapping()
	ph := m.Recall(`he said "hi"`, "TERM")
	restored := RestoreJSONBytes([]byte("prefix "+ph+" suffix"), m)
	assert.Equal(t, `prefix he said "hi" suffix`, string(restored))
}

func TestRestore_NilMapping_Passthrough(t *testing.T) {
	assert.Equal(t, "nothing happens", RestoreString("nothing happens", nil))
	assert.Equal(t, "x", string(RestoreBytes([]byte("x"), nil)))
	assert.Equal(t, `{"a":"b"}`, string(RestoreJSONBytes([]byte(`{"a":"b"}`), nil)))
}

func TestRestoreJSONString_Escape(t *testing.T) {
	m := newMapping()
	// 原文含双引号与反斜杠, 还原进 JSON 字符串须转义。
	ph := m.Recall(`he said "hi"\n`, "TERM")
	out := RestoreJSONString(`{"args":"prefix `+ph+` suffix"}`, m)
	// 占位符替换为转义后的原文: " → \", \ → \\.
	assert.Contains(t, out, `he said \"hi\"\\n`, "引号与反斜杠须 JSON 转义")
	assert.Contains(t, out, `{"args":"prefix `, "其余 JSON 结构保留")
}

func TestRestoreJSONString_UnresolvedLeftAsIs(t *testing.T) {
	m := newMapping()
	unknown := "{{PHONE_bcdfgh}}"
	out := RestoreJSONString(`{"a":"`+unknown+`"}`, m)
	assert.Equal(t, `{"a":"`+unknown+`"}`, out, "未登记占位符原样保留")
}

func TestRestore_RoundTripViaEngine(t *testing.T) {
	e := NewEngine(NewSessionStore())
	res, err := e.Apply("db postgres://u:p4ss@h:5432/d call 13800138000", "s1", enable("CONNSTR", "PHONE"), nil)
	require.NoError(t, err)
	restored := RestoreString(res.Masked, res.Mapping)
	assert.Equal(t, "db postgres://u:p4ss@h:5432/d call 13800138000", restored)
}
