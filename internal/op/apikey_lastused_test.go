package op

import (
	"context"
	"testing"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
)

// TestAPIKeyTouchLastUsedAsyncDedup 验证 RELI-07: 触碰事件去重合并后由后台
// 写入协程批量落库, 停机 flush 能把在途批次写回 DB。
func TestAPIKeyTouchLastUsedAsyncDedup(t *testing.T) {
	ctx := context.Background()
	key := model.APIKey{Name: "lastused-async", APIKey: "sk-test-lastused-async-0001", Enabled: true}
	if err := APIKeyCreate(&key, ctx); err != nil {
		t.Fatalf("APIKeyCreate: %v", err)
	}
	t.Cleanup(func() { _ = APIKeyDelete(key.ID, ctx) })

	APIKeyTouchLastUsed(key.ID)
	APIKeyTouchLastUsed(key.ID) // 去重: 同 key 至少等 flush 后才会再次入队。

	if err := APIKeyTouchLastUsedFlush(ctx); err != nil {
		t.Fatalf("APIKeyTouchLastUsedFlush: %v", err)
	}

	var stored model.APIKey
	if err := db.GetDB().First(&stored, key.ID).Error; err != nil {
		t.Fatalf("read APIKey: %v", err)
	}
	if stored.LastUsedAt == 0 {
		t.Fatal("async writer must persist last_used_at after flush")
	}
	if len(apiKeyTouchPending) != 0 {
		t.Fatalf("pending map after flush = %d, want 0", len(apiKeyTouchPending))
	}
}
