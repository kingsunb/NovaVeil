package op

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
)

// usageBucketTestNow 冻结测试时钟(整点 + 30 分钟), 保证取桶与窗口计算完全确定,
// 用例不会因测试恰好在小时边界运行而 flaky。
var usageBucketTestNow time.Time

func init() {
	usageBucketTestNow = time.Date(2026, 9, 7, 12, 30, 0, 0, time.UTC)
	usageBucketNow = func() time.Time { return usageBucketTestNow }
}

// recordUsageAt 在受控时刻落桶, 便于构造历史窗口数据。
func recordUsageAt(t *testing.T, at time.Time, modelName string, input, output int64) {
	t.Helper()
	bucket := model.UsageBucket{
		BucketAt:     at.UTC().Truncate(UsageBucketHour),
		ModelName:    modelName,
		InputTokens:  input,
		OutputTokens: output,
	}
	if err := db.GetDB().Create(&bucket).Error; err != nil {
		t.Fatalf("create usage bucket: %v", err)
	}
}

// clearUsageBuckets 清空桶表, 保证用例间互不残留。
func clearUsageBuckets(t *testing.T) {
	t.Helper()
	if err := db.GetDB().Where("1 = 1").Delete(&model.UsageBucket{}).Error; err != nil {
		t.Fatalf("clear usage buckets: %v", err)
	}
}

func TestRecordUsageBucketAccumulates(t *testing.T) {
	clearUsageBuckets(t)

	// 同一 (小时, 模型) 桶两次终态累加成一行
	RecordUsageBucket("gpt-4o", 100, 50, 10, 5, 0.001, 1200)
	RecordUsageBucket("gpt-4o", 30, 20, 5, 0, 0.0005, 800)

	// 相邻小时的桶独立成行
	recordUsageAt(t, usageBucketTestNow.Add(time.Hour), "gpt-4o", 5, 5)

	rows := make([]model.UsageBucket, 0)
	if err := db.GetDB().Order("bucket_at ASC").Find(&rows).Error; err != nil {
		t.Fatalf("query usage buckets: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("期望 2 行(当前小时累加 + 下一小时), 实际 %d 行", len(rows))
	}
	if rows[0].InputTokens != 130 || rows[0].OutputTokens != 70 {
		t.Fatalf("当前小时桶应累加为 130/70, 实际 %d/%d", rows[0].InputTokens, rows[0].OutputTokens)
	}
	if rows[1].InputTokens != 5 || rows[1].OutputTokens != 5 {
		t.Fatalf("相邻小时桶应独立为 5/5, 实际 %d/%d", rows[1].InputTokens, rows[1].OutputTokens)
	}
}

func TestRecordUsageBucketSkips(t *testing.T) {
	clearUsageBuckets(t)

	// 双零 / 空模型名 → 不写行
	RecordUsageBucket("", 100, 50, 0, 0, 0, 0)
	RecordUsageBucket("gpt-4o", 0, 0, 0, 0, 0, 0)
	RecordUsageBucket("", 0, 0, 0, 0, 0, 0)

	var count int64
	if err := db.GetDB().Model(&model.UsageBucket{}).Count(&count).Error; err != nil {
		t.Fatalf("count usage buckets: %v", err)
	}
	if count != 0 {
		t.Fatalf("空模型/零用量不应产生行, 实际 %d 行", count)
	}
}

func TestUsageTrendPointsRanges(t *testing.T) {
	clearUsageBuckets(t)

	now := usageBucketTestNow
	// 24h 档末点(当前小时) + 一个 26 小时前的桶(在窗口外)
	recordUsageAt(t, now, "gpt-4o", 100, 40)
	recordUsageAt(t, now, "claude", 10, 5)
	recordUsageAt(t, now.Add(-26*time.Hour), "gpt-4o", 999, 999) // 窗口外, 不应计入

	points, err := UsageTrendPoints(context.Background(), "24h")
	if err != nil {
		t.Fatalf("24h 查询不应失败: %v", err)
	}
	if len(points) != 24 {
		t.Fatalf("24h 应 24 点, 实际 %d", len(points))
	}
	// 单调递增检查
	for i := 1; i < len(points); i++ {
		if points[i].T <= points[i-1].T {
			t.Fatalf("t 应严格单调递增: 第 %d 点 %d <= %d", i, points[i].T, points[i-1].T)
		}
	}
	// 末点 = 当前小时的两模型合计
	last := points[len(points)-1]
	if last.In != 110 || last.Out != 45 {
		t.Fatalf("末点应聚合两模型 110/45, 实际 %d/%d", last.In, last.Out)
	}
	// 窗口外桶不计入任何点
	totalIn := int64(0)
	for _, p := range points {
		totalIn += p.In
	}
	if totalIn != 110 {
		t.Fatalf("全序列 input 合计应为 110(不含窗口外), 实际 %d", totalIn)
	}

	// 7d: 28 点, 步长 6h
	p7, err := UsageTrendPoints(context.Background(), "7d")
	if err != nil {
		t.Fatalf("7d 查询不应失败: %v", err)
	}
	if len(p7) != 28 {
		t.Fatalf("7d 应 28 点, 实际 %d", len(p7))
	}
	if d := p7[1].T - p7[0].T; d != 6*time.Hour.Milliseconds() {
		t.Fatalf("7d 步长应 6h, 实际 %dms", d)
	}

	// 30d: 30 点, 步长 24h; 窗口起点 = now - 29*24h, 26h 前的桶在窗口尾部的
	// 第 28 点(now-24h 起 24h 步长覆盖 24h~48h 前的区间)
	p30, err := UsageTrendPoints(context.Background(), "30d")
	if err != nil {
		t.Fatalf("30d 查询不应失败: %v", err)
	}
	if len(p30) != 30 {
		t.Fatalf("30d 应 30 点, 实际 %d", len(p30))
	}
	if d := p30[1].T - p30[0].T; d != 24*time.Hour.Milliseconds() {
		t.Fatalf("30d 步长应 24h, 实际 %dms", d)
	}
	// 26h 前的桶: idx = (now - 26h - start)/24h = 693h/24h = 28, 落在窗口尾部
	// 第 28 点(30d 档步长 24h, 末点覆盖 now-24h..now 之前)
	if p30[28].In != 999 || p30[28].Out != 999 {
		t.Fatalf("30d 第 28 点应含 26h 前窗口内桶 999/999, 实际 %d/%d", p30[28].In, p30[28].Out)
	}
}

func TestUsageTrendPointsEmptyDB(t *testing.T) {
	clearUsageBuckets(t)

	for _, r := range []string{"24h", "7d", "30d"} {
		points, err := UsageTrendPoints(context.Background(), r)
		if err != nil {
			t.Fatalf("%s 空库查询不应失败: %v", r, err)
		}
		expected := map[string]int{"24h": 24, "7d": 28, "30d": 30}[r]
		if len(points) != expected {
			t.Fatalf("%s 空库应 %d 点, 实际 %d", r, expected, len(points))
		}
		for i, p := range points {
			if p.In != 0 || p.Out != 0 {
				t.Fatalf("%s 第 %d 点空库应为 0, 实际 %d/%d", r, i, p.In, p.Out)
			}
		}
	}
}

func TestRecordUsageBucketClampsNegative(t *testing.T) {
	clearUsageBuckets(t)

	// 负输入 + 正常输出: 输入钳为 0, 输出正常累加, 不因负值跳到跳过条件
	RecordUsageBucket("gpt-4o", -100, 50, 0, 0, 0, 0)

	row := model.UsageBucket{}
	if err := db.GetDB().Where("model_name = ?", "gpt-4o").First(&row).Error; err != nil {
		t.Fatalf("查询桶失败: %v", err)
	}
	if row.InputTokens != 0 || row.OutputTokens != 50 {
		t.Fatalf("负输入应钳为 0 输入、50 输出, 实际 %d/%d", row.InputTokens, row.OutputTokens)
	}
}

func TestUsageTrendPointsInvalidRangeFallsBack(t *testing.T) {
	clearUsageBuckets(t)

	// 非法档位应回退到 7d: 仍正常查询(不报错), 返回 28 个零值点。
	points, err := UsageTrendPoints(context.Background(), "invalid")
	if err != nil {
		t.Fatalf("非法档位回退查询不应失败: %v", err)
	}
	if len(points) != 28 {
		t.Fatalf("非法档位应回退 7d 的 28 点, 实际 %d", len(points))
	}
	// 三个合法档位应通过校验
	if !ValidUsageRange("24h") || !ValidUsageRange("7d") || !ValidUsageRange("30d") {
		t.Fatal("三个合法档位应通过校验")
	}
	// 非法档位不应通过校验
	if ValidUsageRange("1h") || ValidUsageRange("") {
		t.Fatal("非法档位不应通过校验")
	}
}

func TestUsageTotals(t *testing.T) {
	clearUsageBuckets(t)

	recordUsageAt(t, usageBucketTestNow, "gpt-4o", 100, 40)
	recordUsageAt(t, usageBucketTestNow.Add(-2*time.Hour), "claude", 60, 30)

	in, out, err := UsageTotals(context.Background())
	if err != nil {
		t.Fatalf("全表总量查询不应失败: %v", err)
	}
	if in != 160 || out != 70 {
		t.Fatalf("全表总量应为 160/70, 实际 %d/%d", in, out)
	}
}

// TestUsageKPIsByRange 验证按时间窗口聚合 KPI: token 用量与请求计数都只计入窗口内分桶,
// forever 不加窗口条件(全表), 非法档位回退 forever。
func TestUsageKPIsByRange(t *testing.T) {
	clearUsageBuckets(t)

	// 当前小时两次终态累加: input=200/output=80/request_count=2(RecordUsageBucket 每次计 1)
	RecordUsageBucket("gpt-4o", 120, 50, 0, 0, 0, 0)
	RecordUsageBucket("gpt-4o", 80, 30, 0, 0, 0, 0)
	// 窗口外的桶(26h 前): 不应计入 24h 窗口, 但计入 forever
	recordUsageAt(t, usageBucketTestNow.Add(-26*time.Hour), "claude", 999, 999)

	// 24h 窗口: 仅当前小时桶
	in, out, req, err := UsageKPIsByRange(context.Background(), "24h")
	if err != nil {
		t.Fatalf("24h KPI 查询不应失败: %v", err)
	}
	if in != 200 || out != 80 {
		t.Fatalf("24h token 应 200/80, 实际 %d/%d", in, out)
	}
	if req != 2 {
		t.Fatalf("24h 请求计数应 2, 实际 %d", req)
	}

	// forever: 全表(默认永久保留, 无 cutoff), 含 26h 前桶; 其 request_count=0(recordUsageAt 直写)
	in, out, req, err = UsageKPIsByRange(context.Background(), "forever")
	if err != nil {
		t.Fatalf("forever KPI 查询不应失败: %v", err)
	}
	if in != 1199 || out != 1079 {
		t.Fatalf("forever token 应 1199/1079, 实际 %d/%d", in, out)
	}
	if req != 2 {
		t.Fatalf("forever 请求计数应 2(26h 前桶 request_count=0), 实际 %d", req)
	}

	// 非法档位回退 forever: 与 forever 结果一致
	in2, out2, req2, err := UsageKPIsByRange(context.Background(), "bogus")
	if err != nil {
		t.Fatalf("非法档位回退查询不应失败: %v", err)
	}
	if in2 != in || out2 != out || req2 != req {
		t.Fatalf("非法档位应回退 forever, 实际 %d/%d/%d", in2, out2, req2)
	}
}

// TestUsageWindowSince 验证窗口下界计算: forever/非法返回零值, 其余返回当前小时减 span。
func TestUsageWindowSince(t *testing.T) {
	if s := UsageWindowSince("forever"); !s.IsZero() {
		t.Fatalf("forever 应返回零值时间, 实际 %v", s)
	}
	if s := UsageWindowSince("bogus"); !s.IsZero() {
		t.Fatalf("非法档位应返回零值时间, 实际 %v", s)
	}
	hour := usageBucketTestNow.UTC().Truncate(time.Hour)
	if s := UsageWindowSince("24h"); !s.Equal(hour.Add(-24 * time.Hour)) {
		t.Fatalf("24h 下界应为 当前小时-24h, 实际 %v", s)
	}
	if s := UsageWindowSince("7d"); !s.Equal(hour.Add(-7 * 24 * time.Hour)) {
		t.Fatalf("7d 下界应为 当前小时-7d, 实际 %v", s)
	}
}

func TestUsageTotalsByModel(t *testing.T) {
	clearUsageBuckets(t)

	recordUsageAt(t, usageBucketTestNow, "gpt-4o", 100, 40)
	recordUsageAt(t, usageBucketTestNow.Add(-2*time.Hour), "gpt-4o", 60, 30)
	recordUsageAt(t, usageBucketTestNow.Add(-3*time.Hour), "claude", 10, 5)

	byModel, err := UsageTotalsByModel(context.Background())
	if err != nil {
		t.Fatalf("按模型聚合查询不应失败: %v", err)
	}
	if len(byModel) != 2 {
		t.Fatalf("应 2 个模型, 实际 %d", len(byModel))
	}
	if byModel[0].Name != "gpt-4o" {
		t.Fatalf("合计降序首位应为 gpt-4o, 实际 %s", byModel[0].Name)
	}
	if byModel[0].Input != 160 || byModel[0].Output != 70 {
		t.Fatalf("gpt-4o 应聚合 160/70, 实际 %d/%d", byModel[0].Input, byModel[0].Output)
	}
	if byModel[1].Name != "claude" || byModel[1].Input != 10 || byModel[1].Output != 5 {
		t.Fatalf("claude 应 10/5, 实际 %+v", byModel[1])
	}
}

func TestUsageTrendPointJSONShape(t *testing.T) {
	point := UsageTrendPoint{T: 1752249600000, In: 12000, Out: 5000}
	raw, err := json.Marshal(point)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	for _, field := range []string{`"t":1752249600000`, `"in":12000`, `"out":5000`} {
		if !strings.Contains(s, field) {
			t.Fatalf("JSON 应含 %s, 实际 %s", field, s)
		}
	}
}

// TestUsageReadsReturnErrorOnQueryFailure 验证读取函数在 DB 查询失败时返回非 nil error,
// 而非静默返回全零/空结果——让调用方(handler/前端)能区分"无流量"与"统计不可用"。
// 做法: 临时删除 usage_buckets 表迫使查询失败, 测试结束在 t.Cleanup 中重建表。
func TestUsageReadsReturnErrorOnQueryFailure(t *testing.T) {
	clearUsageBuckets(t)

	if err := db.GetDB().Migrator().DropTable(&model.UsageBucket{}); err != nil {
		t.Fatalf("drop usage_buckets table: %v", err)
	}
	t.Cleanup(func() {
		if err := db.GetDB().AutoMigrate(&model.UsageBucket{}); err != nil {
			t.Fatalf("restore usage_buckets table: %v", err)
		}
	})

	ctx := context.Background()

	if _, _, err := UsageTotals(ctx); err == nil {
		t.Fatal("UsageTotals 在表缺失时应返回错误, 而非静默全零")
	}
	if _, err := UsageTotalsByModel(ctx); err == nil {
		t.Fatal("UsageTotalsByModel 在表缺失时应返回错误, 而非静默空结果")
	}
	points, err := UsageTrendPoints(ctx, "24h")
	if err == nil {
		t.Fatal("UsageTrendPoints 在表缺失时应返回错误, 而非静默全零")
	}
	// 即便查询失败, 仍应返回按档位填好的零值时间轴(保留 X 轴连续)。
	if len(points) != 24 {
		t.Fatalf("UsageTrendPoints 查询失败时仍应返回 24 个零值点, 实际 %d", len(points))
	}
}
