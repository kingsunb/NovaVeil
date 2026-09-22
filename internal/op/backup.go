package op

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kingsunb/NovaVeil/internal/db"
	"github.com/kingsunb/NovaVeil/internal/model"
	"github.com/kingsunb/NovaVeil/internal/seal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const dbDumpVersion = 4

// maxSyncLLMIntervalHours 同步间隔上限(一年): 与 setting handler 一致,
// 超过会溢出 time.Duration 且无运维意义。导入校验需与正常写接口同规则。
const maxSyncLLMIntervalHours = 8760

// DBExportAll 导出完整数据库内容。
// 在单一事务内读取所有表, 获得一致性快照, 避免并发写入导致跨表引用不一致
// (如导出 channels 后、导出 channel_models 前有渠道被删除)。
func DBExportAll(ctx context.Context) (*model.DBDump, error) {
	d := &model.DBDump{
		Version:    dbDumpVersion,
		ExportedAt: time.Now().UTC(),
		Note:       model.DBDumpSensitiveNote,
	}

	conn := db.GetDB().WithContext(ctx)
	err := conn.Transaction(func(tx *gorm.DB) error {
		if err := tx.Find(&d.Channels).Error; err != nil {
			return fmt.Errorf("导出渠道失败: %w", err)
		}
		if err := tx.Find(&d.Groups).Error; err != nil {
			return fmt.Errorf("导出分组失败: %w", err)
		}
		if err := tx.Find(&d.ChannelModels).Error; err != nil {
			return fmt.Errorf("导出渠道模型失败: %w", err)
		}
		if err := tx.Find(&d.GroupItems).Error; err != nil {
			return fmt.Errorf("导出分组成员失败: %w", err)
		}
		if err := tx.Find(&d.APIKeys).Error; err != nil {
			return fmt.Errorf("导出 API key 失败: %w", err)
		}
		if err := tx.Find(&d.Settings).Error; err != nil {
			return fmt.Errorf("导出设置失败: %w", err)
		}
		if err := tx.Find(&d.ClientStats).Error; err != nil {
			return fmt.Errorf("导出客户端统计失败: %w", err)
		}
		if err := tx.Find(&d.UsageBuckets).Error; err != nil {
			return fmt.Errorf("导出用量汇总失败: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	d.Settings = filterSecretSettings(d.Settings)
	if err := revealKeysForExport(d); err != nil {
		return nil, err
	}
	if err := redactHeaderTemplatesForExport(d.Settings); err != nil {
		return nil, err
	}
	redactNonKeyCredentialsForExport(d)
	return d, nil
}

// revealKeysForExport 把库内 nv1: 密文解成明文再写入备份。
// 渠道旧式 Key、多 Key 和 API Key 都解开。没有 nv1: 前缀的存量明文保持原样。
// 只拿到数据库文件时这些密文无法在另一台机器使用，备份要能直接还原调用。
func revealKeysForExport(d *model.DBDump) error {
	for i := range d.Channels {
		ch := &d.Channels[i]
		if ch.Key != "" {
			plain, err := seal.Open(ch.Key)
			if err != nil {
				return fmt.Errorf("导出渠道 %d(%s) 的 Key 失败: %w", ch.ID, ch.Name, err)
			}
			ch.Key = plain
		}
		for j := range ch.Keys {
			if ch.Keys[j].Key == "" {
				continue
			}
			plain, err := seal.Open(ch.Keys[j].Key)
			if err != nil {
				return fmt.Errorf("导出渠道 %d(%s) 的 Key 失败: %w", ch.ID, ch.Name, err)
			}
			ch.Keys[j].Key = plain
		}
	}
	for i := range d.APIKeys {
		if d.APIKeys[i].APIKey == "" {
			continue
		}
		plain, err := seal.Open(d.APIKeys[i].APIKey)
		if err != nil {
			return fmt.Errorf("导出 API Key %d(%s) 失败: %w", d.APIKeys[i].ID, d.APIKeys[i].Name, err)
		}
		d.APIKeys[i].APIKey = plain
	}
	return nil
}

// dropRedactedNonKeyCredentials 丢掉导出打码后的代理和自定义头。
// 这些字段不是明文 Key, 导入时不恢复, 也不把 "****" 密封成真实头值。
func dropRedactedNonKeyCredentials(ch *model.Channel) {
	if ch == nil {
		return
	}
	if ch.ChannelProxy != nil && *ch.ChannelProxy == redactedSecret {
		ch.ChannelProxy = nil
	}
	if len(ch.CustomHeader) == 0 {
		return
	}
	kept := make([]model.CustomHeader, 0, len(ch.CustomHeader))
	for _, header := range ch.CustomHeader {
		if header.HeaderValue == redactedSecret {
			continue
		}
		kept = append(kept, header)
	}
	ch.CustomHeader = kept
}

// omitUnrestorableSettings 去掉整份都是打码头值的请求头模板。
// 现行导出会把模板头值写成 "****"。按 key upsert 会覆盖线上仍在使用的模板。
func omitUnrestorableSettings(rows []model.Setting) []model.Setting {
	if len(rows) == 0 {
		return rows
	}
	kept := make([]model.Setting, 0, len(rows))
	for _, row := range rows {
		if row.Key == model.SettingKeyHeaderTemplates && headerTemplateFullyRedacted(row.Value) {
			continue
		}
		kept = append(kept, row)
	}
	return kept
}

func headerTemplateFullyRedacted(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	var templates []model.HeaderTemplate
	if err := json.Unmarshal([]byte(raw), &templates); err != nil {
		return false
	}
	sawValue := false
	for _, template := range templates {
		for _, header := range template.Headers {
			if header.HeaderValue == "" {
				continue
			}
			sawValue = true
			if header.HeaderValue != redactedSecret {
				return false
			}
		}
	}
	return sawValue
}

// redactNonKeyCredentialsForExport 备份里仍打码的是代理地址和自定义头值，不是 Key。
// 自定义头保留 header_key，只把非空 header_value 打成精确哨兵。
func redactNonKeyCredentialsForExport(d *model.DBDump) {
	for i := range d.Channels {
		if d.Channels[i].ChannelProxy != nil && *d.Channels[i].ChannelProxy != redactedSecret {
			redacted := redactedSecret
			d.Channels[i].ChannelProxy = &redacted
		}
		for j := range d.Channels[i].CustomHeader {
			if d.Channels[i].CustomHeader[j].HeaderValue != "" {
				d.Channels[i].CustomHeader[j].HeaderValue = redactedSecret
			}
		}
	}
}

// redactHeaderTemplatesForExport 解开 header_templates(密文或存量明文)后,
// 保留模板名与头名, 把非空头值打成 "****"。auth_jwt_secret、proxy_url、proxy_pool
// 已由 filterSecretSettings 整行剔除, 不会出现在备份里。
func redactHeaderTemplatesForExport(rows []model.Setting) error {
	for i := range rows {
		if rows[i].Key != model.SettingKeyHeaderTemplates {
			continue
		}
		plain, err := openSettingValue(rows[i].Key, rows[i].Value)
		if err != nil {
			return fmt.Errorf("导出请求头模板失败: %w", err)
		}
		masked, err := maskHeaderTemplateSecrets(plain)
		if err != nil {
			return err
		}
		rows[i].Value = masked
	}
	return nil
}

// maskHeaderTemplateSecrets 打码头模板里的头值, 不删除模板结构。
func maskHeaderTemplateSecrets(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw, nil
	}
	var templates []model.HeaderTemplate
	if err := json.Unmarshal([]byte(raw), &templates); err != nil {
		return "", fmt.Errorf("导出请求头模板失败: %w", err)
	}
	for i := range templates {
		for j := range templates[i].Headers {
			if templates[i].Headers[j].HeaderValue != "" {
				templates[i].Headers[j].HeaderValue = redactedSecret
			}
		}
	}
	out, err := json.Marshal(templates)
	if err != nil {
		return "", fmt.Errorf("导出请求头模板失败: %w", err)
	}
	return string(out), nil
}

// DBImportValidationError 在导入预检发现校验问题时返回, 携带完整预检结果供 handler 展示。
type DBImportValidationError struct {
	Preview *model.DBImportPreview
}

func (e *DBImportValidationError) Error() string {
	if e.Preview == nil {
		return "导入校验失败"
	}
	var parts []string
	for _, s := range e.Preview.SettingsIssues {
		parts = append(parts, s)
	}
	for _, r := range e.Preview.InvalidRefs {
		parts = append(parts, fmt.Sprintf("%s: %s", r.Table, r.Desc))
	}
	for _, c := range e.Preview.Cycles {
		parts = append(parts, c)
	}
	if len(parts) == 0 {
		return "导入校验失败"
	}
	return fmt.Sprintf("导入校验失败, 共 %d 项: %s", len(parts), strings.Join(parts, "; "))
}

// DBImportPreview 执行导入预检(dry-run): 在只读事务内读取当前库状态, 对照 dump
// 计算新增/跳过/冲突/无效引用/设置校验问题/循环引用, 不写入任何数据。
func DBImportPreview(ctx context.Context, dump *model.DBDump) (*model.DBImportPreview, error) {
	if dump == nil {
		return nil, fmt.Errorf("备份内容为空")
	}
	if err := checkDumpVersion(dump.Version); err != nil {
		return nil, err
	}

	var preview *model.DBImportPreview
	err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		p, err := analyzeImport(tx, dump)
		if err != nil {
			return err
		}
		preview = p
		return nil // 无写入, 提交空事务
	})
	if err != nil {
		return nil, err
	}
	return preview, nil
}

// DBImportIncremental 校验并增量导入备份内容。
// 所有表统一使用 DO NOTHING 冲突策略(主键/唯一键已存在则跳过), 仅 settings 按 key upsert。
// 导入前在事务内执行与正常写接口相同的领域校验; 校验不通过时事务回滚, 返回 DBImportValidationError。
func DBImportIncremental(ctx context.Context, dump *model.DBDump) (*model.DBImportResult, error) {
	if dump == nil {
		return nil, fmt.Errorf("备份内容为空")
	}
	if err := checkDumpVersion(dump.Version); err != nil {
		return nil, err
	}

	conn := db.GetDB().WithContext(ctx)
	res := &model.DBImportResult{RowsAffected: map[string]int64{}}
	err := conn.Transaction(func(tx *gorm.DB) error {
		// 1. 预检/校验: 读取当前库状态, 验证设置值、引用关系、循环引用、Key 唯一性。
		preview, err := analyzeImport(tx, dump)
		if err != nil {
			return err
		}
		if !preview.CanImport {
			return &DBImportValidationError{Preview: preview}
		}

		// 2. 归一化: 渠道密钥、分组模式与 Relay 配置。
		for i := range dump.Channels {
			keys, err := normalizeChannelKeys(dump.Channels[i].Keys)
			if err != nil {
				return fmt.Errorf("导入渠道失败: 渠道 %d 的密钥归一化出错: %w", dump.Channels[i].ID, err)
			}
			dump.Channels[i].Keys = keys
		}
		normalizeImportGroups(dump)

		// 3. 写入: 所有基础表 DO NOTHING, settings 按 key upsert。
		// 敏感字段在落库前统一加密; 旧版未加密 dump 与已含掩码的 dump 同样经过该路径。
		sealedChannels := make([]model.Channel, len(dump.Channels))
		for i, ch := range dump.Channels {
			sealedChannels[i] = ch
			sealedChannel, sErr := sealChannelForDB(ch)
			if sErr != nil {
				return fmt.Errorf("导入渠道失败, 渠道 %d: %w", ch.ID, sErr)
			}
			sealedChannels[i] = sealedChannel
		}
		if n, err := createDoNothing(tx, sealedChannels); err != nil {
			return fmt.Errorf("导入渠道失败: %w", err)
		} else {
			res.RowsAffected["channels"] = n
		}
		if n, err := createDoNothing(tx, dump.Groups); err != nil {
			return fmt.Errorf("导入分组失败: %w", err)
		} else {
			res.RowsAffected["groups"] = n
		}
		// channel_models 与 channels 一致使用 DO NOTHING: 避免外库同 ID 的渠道模型
		// 覆盖本地渠道的实际模型/路由(STA-06 核心修复)。
		if n, err := createDoNothing(tx, dump.ChannelModels); err != nil {
			return fmt.Errorf("导入渠道模型失败: %w", err)
		} else {
			res.RowsAffected["channel_models"] = n
		}
		if n, err := createDoNothing(tx, dump.GroupItems); err != nil {
			return fmt.Errorf("导入分组成员失败: %w", err)
		} else {
			res.RowsAffected["group_items"] = n
		}
		sealedAPIKeys := make([]model.APIKey, len(dump.APIKeys))
		for i, ak := range dump.APIKeys {
			sealedAPIKeys[i] = ak
			sealedValue, sErr := seal.Seal(ak.APIKey)
			if sErr != nil {
				return fmt.Errorf("导入 API key 失败, id=%d: %w", ak.ID, sErr)
			}
			sealedAPIKeys[i].APIKey = sealedValue
		}
		if n, err := createDoNothing(tx, sealedAPIKeys); err != nil {
			return fmt.Errorf("导入 API key 失败: %w", err)
		} else {
			res.RowsAffected["api_keys"] = n
		}
		sealedSettings, sealErr := sealSettingsForDB(omitUnrestorableSettings(filterSecretSettings(dump.Settings)))
		if sealErr != nil {
			return fmt.Errorf("导入设置失败: %w", sealErr)
		}
		if n, err := createUpsertSettings(tx, sealedSettings); err != nil {
			return fmt.Errorf("导入设置失败: %w", err)
		} else {
			res.RowsAffected["settings"] = n
		}
		if n, err := createDoNothing(tx, dump.ClientStats); err != nil {
			return fmt.Errorf("导入客户端统计失败: %w", err)
		} else {
			res.RowsAffected["client_stats"] = n
		}
		if n, err := createDoNothing(tx, dump.UsageBuckets); err != nil {
			return fmt.Errorf("导入用量汇总失败: %w", err)
		} else {
			res.RowsAffected["usage_buckets"] = n
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func checkDumpVersion(version int) error {
	// 版本 0 视为无版本信息的旧导出, 按当前版本兼容导入;
	// v3 与 v4 结构兼容(v4 仅新增 client_stats/usage_buckets 两个可选字段),
	// 允许 v3 导入以兼容旧备份文件。
	compatibleVersions := map[int]struct{}{0: {}, 3: {}, dbDumpVersion: {}}
	if _, ok := compatibleVersions[version]; !ok {
		return fmt.Errorf("不支持的备份版本: %d, 当前支持版本: %d", version, dbDumpVersion)
	}
	return nil
}

// normalizeImportGroups 对 dump 中的分组执行与正常写接口相同的归一化:
// 空模式默认 manual, RelayConfig 补齐空值。模式合法性已由 analyzeImport 校验。
func normalizeImportGroups(dump *model.DBDump) {
	for i := range dump.Groups {
		if dump.Groups[i].Mode == "" {
			dump.Groups[i].Mode = model.GroupModeManual
		}
		model.NormalizeGroupRelayConfig(&dump.Groups[i].RelayConfig)
	}
}

// analyzeImport 在事务内读取当前库状态, 对照 dump 执行全部领域校验并生成预检结果。
// 不写入任何数据。校验项:
//   - 设置值: 与 Setting.Validate 同规则(设置值、CORS、同步间隔、随机头等)
//   - 渠道密钥: normalizeChannelKeys 唯一性
//   - 渠道模型引用: ChannelID 指向有效渠道(现存或 dump 新增)
//   - 分组成员引用: ChannelModelID / RefGroupName 指向有效目标
//   - 分组引用循环: 有效态分组引用链无环
//   - API Key 领域校验与唯一性: 与正常写接口相同的最小长度校验, 脱敏 Key 拒绝;
//     dump 内及与现存库无重复明文
//   - 渠道 BaseURL 校验: 非 custom 渠道强制 http/https + host + 无 userinfo 格式
//   - 分组模式: manual / failover / 空(默认)
func analyzeImport(tx *gorm.DB, dump *model.DBDump) (*model.DBImportPreview, error) {
	preview := &model.DBImportPreview{
		Summary: model.DBImportSummary{
			New:     map[string]int64{},
			Skipped: map[string]int64{},
			Updated: map[string]int64{},
		},
	}

	// --- 读取现存库状态(事务内一致性快照) ---
	var existingChannels []model.Channel
	if err := tx.Find(&existingChannels).Error; err != nil {
		return nil, fmt.Errorf("读取现存渠道失败: %w", err)
	}
	var existingChannelModels []model.ChannelModel
	if err := tx.Find(&existingChannelModels).Error; err != nil {
		return nil, fmt.Errorf("读取现存渠道模型失败: %w", err)
	}
	var existingGroups []model.Group
	if err := tx.Preload("Items").Find(&existingGroups).Error; err != nil {
		return nil, fmt.Errorf("读取现存分组失败: %w", err)
	}
	var existingAPIKeys []model.APIKey
	if err := tx.Find(&existingAPIKeys).Error; err != nil {
		return nil, fmt.Errorf("读取现存 API key 失败: %w", err)
	}
	var existingSettings []model.Setting
	if err := tx.Find(&existingSettings).Error; err != nil {
		return nil, fmt.Errorf("读取现存设置失败: %w", err)
	}
	var existingClientStats []model.ClientStat
	if err := tx.Find(&existingClientStats).Error; err != nil {
		return nil, fmt.Errorf("读取现存客户端统计失败: %w", err)
	}
	var existingUsageBuckets []model.UsageBucket
	if err := tx.Find(&existingUsageBuckets).Error; err != nil {
		return nil, fmt.Errorf("读取现存用量汇总失败: %w", err)
	}

	// 现存库敏感字段以密文落库, 预检的命名/密钥唯一性比较基于明文;
	// 游标读回全部渠道/API key 后统一解密到内存(审计 SEC-04 迁移兼容)。
	for i := range existingChannels {
		if err := openChannelForCache(&existingChannels[i]); err != nil {
			return nil, err
		}
	}
	for i := range existingAPIKeys {
		plain, err := seal.Open(existingAPIKeys[i].APIKey)
		if err != nil {
			return nil, fmt.Errorf("读取现存 API key 失败: %w", err)
		}
		existingAPIKeys[i].APIKey = plain
	}

	// --- 构建现存库查找表 ---
	existingChannelIDs := make(map[int]struct{}, len(existingChannels))
	existingChannelNames := make(map[string]struct{}, len(existingChannels))
	for _, ch := range existingChannels {
		existingChannelIDs[ch.ID] = struct{}{}
		existingChannelNames[ch.Name] = struct{}{}
	}

	existingChannelModelIDs := make(map[int]struct{}, len(existingChannelModels))
	existingChannelModelNatural := make(map[string]int, len(existingChannelModels)) // "channel_id\x00name" → id
	for _, cm := range existingChannelModels {
		existingChannelModelIDs[cm.ID] = struct{}{}
		existingChannelModelNatural[channelModelNaturalKey(cm)] = cm.ID
	}

	existingGroupIDs := make(map[int]struct{}, len(existingGroups))
	existingGroupNames := make(map[string]int, len(existingGroups)) // name → id
	existingGroupItemIDs := make(map[int]struct{})
	for _, g := range existingGroups {
		existingGroupIDs[g.ID] = struct{}{}
		existingGroupNames[g.Name] = g.ID
		for _, item := range g.Items {
			existingGroupItemIDs[item.ID] = struct{}{}
		}
	}

	existingAPIKeyIDs := make(map[int]struct{}, len(existingAPIKeys))
	existingAPIKeyValues := make(map[string]struct{}, len(existingAPIKeys))
	for _, ak := range existingAPIKeys {
		existingAPIKeyIDs[ak.ID] = struct{}{}
		existingAPIKeyValues[ak.APIKey] = struct{}{}
	}

	existingSettingKeys := make(map[string]struct{}, len(existingSettings))
	for _, s := range existingSettings {
		existingSettingKeys[string(s.Key)] = struct{}{}
	}

	existingClientStatIPs := make(map[string]struct{}, len(existingClientStats))
	for _, cs := range existingClientStats {
		existingClientStatIPs[cs.IP] = struct{}{}
	}

	existingUsageBucketIDs := make(map[int64]struct{}, len(existingUsageBuckets))
	existingUsageBucketNatural := make(map[string]int64, len(existingUsageBuckets)) // "bucket_at|model_name" → id
	for _, ub := range existingUsageBuckets {
		existingUsageBucketIDs[ub.ID] = struct{}{}
		existingUsageBucketNatural[usageBucketNaturalKey(ub)] = ub.ID
	}

	// --- 设置校验(与正常写接口同规则) ---
	settingsFiltered := filterSecretSettings(dump.Settings)
	for _, s := range settingsFiltered {
		if err := s.Validate(); err != nil {
			preview.SettingsIssues = append(preview.SettingsIssues,
				fmt.Sprintf("设置 %q 校验失败: %v", s.Key, err))
		}
		// sync_llm_interval 上限: 与 setSetting handler 一致, 超大值乘 time.Hour
		// 会整型溢出成负间隔, 导入后 task.Update 会删除同步任务且运行期无法恢复。
		if s.Key == model.SettingKeySyncLLMInterval {
			if hours, err := strconv.Atoi(s.Value); err != nil || hours < 1 || hours > maxSyncLLMIntervalHours {
				preview.SettingsIssues = append(preview.SettingsIssues,
					fmt.Sprintf("设置 %q 需为 1-%d 之间的整数(小时)", s.Key, maxSyncLLMIntervalHours))
			}
		}
	}

	// dump 内敏感字段同样可能是旧版明文或新版密文; 先解到明文再做密钥校验与冲突比较,
	// 导入写入阶段会统一再加密。掩码值 "****" 解不开也无需解, 保持原样走后续流程。
	for i := range dump.Channels {
		if err := openChannelForCache(&dump.Channels[i]); err != nil {
			return nil, fmt.Errorf("读取备份渠道 %d 密钥失败: %w", dump.Channels[i].ID, err)
		}
	}
	for i := range dump.APIKeys {
		plain, err := seal.Open(dump.APIKeys[i].APIKey)
		if err != nil {
			return nil, fmt.Errorf("读取备份 API key 失败: %w", err)
		}
		dump.APIKeys[i].APIKey = plain
	}

	// --- 渠道密钥校验 ---
	for i := range dump.Channels {
		ch := &dump.Channels[i]
		if _, err := normalizeChannelKeys(ch.Keys); err != nil {
			preview.SettingsIssues = append(preview.SettingsIssues,
				fmt.Sprintf("渠道 %d 密钥校验失败: %v", ch.ID, err))
		}
		// 现行导出的渠道 Key 与 API Key 是明文, 可以还原调用。
		// 精确 "****" 的 Key 只来自旧文件或手改, 不能密封成凭据。
		// 代理和自定义头的 "****" 是现行导出的打码结果, 不恢复, 也不因此拒绝整份备份。
		dropRedactedNonKeyCredentials(ch)
		if ch.Key == redactedSecret {
			preview.InvalidRefs = append(preview.InvalidRefs, model.DBImportInvalidRef{
				Table: "channels",
				ID:    ch.ID,
				Desc:  "渠道 Key 已脱敏(****)，无法作为凭据导入；请替换为真实密钥后再导入",
			})
		}
		for _, key := range ch.Keys {
			if key.Key == redactedSecret {
				preview.InvalidRefs = append(preview.InvalidRefs, model.DBImportInvalidRef{
					Table: "channels",
					ID:    ch.ID,
					Desc:  "渠道多 Key 已脱敏(****)，无法作为凭据导入；请替换为真实密钥后再导入",
				})
				break
			}
		}
	}

	// --- 渠道 BaseURL 校验(与正常写接口同规则) ---
	// 非 custom 渠道会被 relay 直接用于上游访问, 这里复用的校验只强制 http/https、
	// host、无 userinfo 格式; 不做目标地址范围限制。
	for _, ch := range dump.Channels {
		if ch.Type == model.ChannelProviderCustom {
			continue
		}
		if err := ValidateChannelEgressBaseURL(ch.BaseURL); err != nil {
			preview.InvalidRefs = append(preview.InvalidRefs, model.DBImportInvalidRef{
				Table: "channels",
				ID:    ch.ID,
				Desc:  fmt.Sprintf("渠道 %d BaseURL 校验失败: %v", ch.ID, err),
			})
		}
	}

	// --- 分组模式校验 ---
	for _, g := range dump.Groups {
		mode := g.Mode
		if mode == "" {
			continue // 空模式将归一化为 manual, 合法
		}
		if mode != model.GroupModeManual && mode != model.GroupModeFailover {
			preview.SettingsIssues = append(preview.SettingsIssues,
				fmt.Sprintf("分组 %d 模式 %q 无效, 仅允许 manual 或 failover", g.ID, mode))
		}
	}

	// --- 构建有效态集合(现存 ∪ dump 新增, DO NOTHING 语义) ---
	effectiveChannelIDs := make(map[int]struct{}, len(existingChannelIDs)+len(dump.Channels))
	for id := range existingChannelIDs {
		effectiveChannelIDs[id] = struct{}{}
	}
	for _, ch := range dump.Channels {
		effectiveChannelIDs[ch.ID] = struct{}{}
	}

	effectiveChannelModelIDs := make(map[int]struct{}, len(existingChannelModelIDs)+len(dump.ChannelModels))
	for id := range existingChannelModelIDs {
		effectiveChannelModelIDs[id] = struct{}{}
	}
	for _, cm := range dump.ChannelModels {
		effectiveChannelModelIDs[cm.ID] = struct{}{}
	}

	// 有效态分组名: 现存分组名 + dump 新增分组名(ID 不冲突者)
	effectiveGroupNames := make(map[string]struct{}, len(existingGroupNames)+len(dump.Groups))
	for name := range existingGroupNames {
		effectiveGroupNames[name] = struct{}{}
	}
	for _, g := range dump.Groups {
		if _, exists := existingGroupIDs[g.ID]; !exists {
			effectiveGroupNames[g.Name] = struct{}{}
		}
	}

	// --- 渠道模型引用校验 ---
	for _, cm := range dump.ChannelModels {
		if _, ok := effectiveChannelIDs[cm.ChannelID]; !ok {
			preview.InvalidRefs = append(preview.InvalidRefs, model.DBImportInvalidRef{
				Table: "channel_models",
				ID:    cm.ID,
				Desc:  fmt.Sprintf("渠道模型 id=%d 引用的渠道 id=%d 不存在", cm.ID, cm.ChannelID),
			})
		}
	}

	// --- 分组成员引用校验 ---
	for _, gi := range dump.GroupItems {
		if gi.ChannelModelID == 0 && strings.TrimSpace(gi.RefGroupName) == "" {
			preview.InvalidRefs = append(preview.InvalidRefs, model.DBImportInvalidRef{
				Table: "group_items",
				ID:    gi.ID,
				Desc:  fmt.Sprintf("分组成员 id=%d 既未引用渠道模型也未引用分组", gi.ID),
			})
			continue
		}
		if gi.ChannelModelID != 0 {
			if _, ok := effectiveChannelModelIDs[gi.ChannelModelID]; !ok {
				preview.InvalidRefs = append(preview.InvalidRefs, model.DBImportInvalidRef{
					Table: "group_items",
					ID:    gi.ID,
					Desc:  fmt.Sprintf("分组成员 id=%d 引用的渠道模型 id=%d 不存在", gi.ID, gi.ChannelModelID),
				})
			}
		}
		if refName := strings.TrimSpace(gi.RefGroupName); refName != "" {
			if _, ok := effectiveGroupNames[refName]; !ok {
				preview.InvalidRefs = append(preview.InvalidRefs, model.DBImportInvalidRef{
					Table: "group_items",
					ID:    gi.ID,
					Desc:  fmt.Sprintf("分组成员 id=%d 引用的分组 %q 不存在", gi.ID, refName),
				})
			}
		}
	}

	// --- 分组引用循环检测 ---
	cycles := detectGroupCycles(existingGroups, existingGroupIDs, existingGroupItemIDs, dump)
	preview.Cycles = cycles

	// --- API Key 领域校验与唯一性 ---
	// 现行导出的 API Key 是明文。精确 "****" 只来自旧文件或手改, 不能当可用密钥导入。
	// 其余 Key 沿用创建/更新接口的 validateAPIKeyCustom 最小长度规则。
	dumpAPIKeyValues := make(map[string]int, len(dump.APIKeys))
	for _, ak := range dump.APIKeys {
		if ak.APIKey == "****" {
			preview.InvalidRefs = append(preview.InvalidRefs, model.DBImportInvalidRef{
				Table: "api_keys",
				ID:    ak.ID,
				Desc:  "API key 已脱敏(****)，无法作为可用密钥导入；请在导入后重新生成或编辑该 Key",
			})
			continue
		}
		if err := validateAPIKeyCustom(ak.APIKey); err != nil {
			preview.SettingsIssues = append(preview.SettingsIssues,
				fmt.Sprintf("API key id=%d 校验失败: %v", ak.ID, err))
			continue
		}
		if prevID, dup := dumpAPIKeyValues[ak.APIKey]; dup {
			preview.InvalidRefs = append(preview.InvalidRefs, model.DBImportInvalidRef{
				Table: "api_keys",
				ID:    ak.ID,
				Desc:  fmt.Sprintf("API key 明文与 dump 内 id=%d 重复", prevID),
			})
		} else {
			dumpAPIKeyValues[ak.APIKey] = ak.ID
		}
	}

	// --- 新增/跳过/冲突统计 ---
	analyzeTableConflict(preview, "channels", len(dump.Channels), func(i int) bool {
		ch := dump.Channels[i]
		if _, ok := existingChannelIDs[ch.ID]; ok {
			return true
		}
		if _, nameClash := existingChannelNames[ch.Name]; nameClash {
			preview.Conflicts = append(preview.Conflicts, model.DBImportConflict{
				Table: "channels", ID: ch.ID,
				Desc: fmt.Sprintf("渠道 id=%d 名称 %q 与现存渠道冲突, 将跳过", ch.ID, ch.Name),
			})
			return true
		}
		return false
	})
	dumpChannelModelNatural := make(map[string]int, len(dump.ChannelModels))
	for _, cm := range dump.ChannelModels {
		key := channelModelNaturalKey(cm)
		if prevID, dup := dumpChannelModelNatural[key]; dup {
			preview.Conflicts = append(preview.Conflicts, model.DBImportConflict{
				Table: "channel_models", ID: cm.ID,
				Desc: fmt.Sprintf("渠道模型 id=%d 与 dump 内 id=%d 的 (channel_id,name) 唯一键冲突, 将跳过", cm.ID, prevID),
			})
		} else {
			dumpChannelModelNatural[key] = cm.ID
		}
	}
	analyzeTableConflict(preview, "channel_models", len(dump.ChannelModels), func(i int) bool {
		cm := dump.ChannelModels[i]
		if _, exists := existingChannelModelIDs[cm.ID]; exists {
			return true
		}
		if conflictID, naturalDup := existingChannelModelNatural[channelModelNaturalKey(cm)]; naturalDup {
			preview.Conflicts = append(preview.Conflicts, model.DBImportConflict{
				Table: "channel_models", ID: cm.ID,
				Desc: fmt.Sprintf("渠道模型 id=%d 的 (channel_id=%d, name=%q) 与现存 id=%d 唯一键冲突, 将跳过", cm.ID, cm.ChannelID, cm.Name, conflictID),
			})
			return true
		}
		if prevID, dup := dumpChannelModelNatural[channelModelNaturalKey(cm)]; dup && prevID != cm.ID {
			// dump 内重复项: 前面的行会被 INSERT(若与现存库不冲突), 后面的行 DO NOTHING 跳过。
			_ = prevID
			return true
		}
		return false
	})
	analyzeTableConflict(preview, "groups", len(dump.Groups), func(i int) bool {
		g := dump.Groups[i]
		if _, ok := existingGroupIDs[g.ID]; ok {
			return true
		}
		if _, nameClash := existingGroupNames[g.Name]; nameClash {
			preview.Conflicts = append(preview.Conflicts, model.DBImportConflict{
				Table: "groups", ID: g.ID,
				Desc: fmt.Sprintf("分组 id=%d 名称 %q 与现存分组冲突, 将跳过", g.ID, g.Name),
			})
			return true
		}
		return false
	})
	analyzeTableConflict(preview, "group_items", len(dump.GroupItems), func(i int) bool {
		_, exists := existingGroupItemIDs[dump.GroupItems[i].ID]
		return exists
	})
	analyzeTableConflict(preview, "api_keys", len(dump.APIKeys), func(i int) bool {
		ak := dump.APIKeys[i]
		if _, ok := existingAPIKeyIDs[ak.ID]; ok {
			return true
		}
		if _, valClash := existingAPIKeyValues[ak.APIKey]; valClash {
			preview.Conflicts = append(preview.Conflicts, model.DBImportConflict{
				Table: "api_keys", ID: ak.ID,
				Desc: fmt.Sprintf("API key id=%d 明文与现存 key 冲突, 将跳过", ak.ID),
			})
			return true
		}
		return false
	})
	// client_stats: 以 IP 为主键, DO NOTHING。
	analyzeTableConflict(preview, "client_stats", len(dump.ClientStats), func(i int) bool {
		_, exists := existingClientStatIPs[dump.ClientStats[i].IP]
		return exists
	})
	// usage_buckets: 以 ID 为主键, 同时有 (bucket_at, model_name) 唯一键, DO NOTHING 均为冲突跳过。
	dumpUsageBucketNatural := make(map[string]int64, len(dump.UsageBuckets))
	for _, ub := range dump.UsageBuckets {
		key := usageBucketNaturalKey(ub)
		if prevID, dup := dumpUsageBucketNatural[key]; dup {
			preview.Conflicts = append(preview.Conflicts, model.DBImportConflict{
				Table: "usage_buckets", ID: int(ub.ID),
				Desc: fmt.Sprintf("用量桶 id=%d 与 dump 内 id=%d 的 (bucket_at, model_name) 唯一键冲突, 将跳过", ub.ID, prevID),
			})
		} else {
			dumpUsageBucketNatural[key] = ub.ID
		}
	}
	analyzeTableConflict(preview, "usage_buckets", len(dump.UsageBuckets), func(i int) bool {
		ub := dump.UsageBuckets[i]
		if _, exists := existingUsageBucketIDs[ub.ID]; exists {
			return true
		}
		if conflictID, naturalDup := existingUsageBucketNatural[usageBucketNaturalKey(ub)]; naturalDup {
			preview.Conflicts = append(preview.Conflicts, model.DBImportConflict{
				Table: "usage_buckets", ID: int(ub.ID),
				Desc: fmt.Sprintf("用量桶 id=%d 的 (bucket_at=%s, model_name=%q) 与现存 id=%d 唯一键冲突, 将跳过", ub.ID, ub.BucketAt.Format(time.RFC3339), ub.ModelName, conflictID),
			})
			return true
		}
		return false
	})
	// settings 按 key upsert: 已存在的 key 计为 Updated, 否则 New。
	for _, s := range settingsFiltered {
		if _, exists := existingSettingKeys[string(s.Key)]; exists {
			preview.Summary.Updated["settings"]++
		} else {
			preview.Summary.New["settings"]++
		}
	}

	// --- 排序冲突与无效引用, 保证输出稳定 ---
	sort.Slice(preview.Conflicts, func(i, j int) bool {
		if preview.Conflicts[i].Table != preview.Conflicts[j].Table {
			return preview.Conflicts[i].Table < preview.Conflicts[j].Table
		}
		return preview.Conflicts[i].ID < preview.Conflicts[j].ID
	})
	sort.Slice(preview.InvalidRefs, func(i, j int) bool {
		if preview.InvalidRefs[i].Table != preview.InvalidRefs[j].Table {
			return preview.InvalidRefs[i].Table < preview.InvalidRefs[j].Table
		}
		return preview.InvalidRefs[i].ID < preview.InvalidRefs[j].ID
	})

	preview.CanImport = len(preview.SettingsIssues) == 0 &&
		len(preview.InvalidRefs) == 0 &&
		len(preview.Cycles) == 0
	return preview, nil
}

// channelModelNaturalKey 组装 channel_models 表的 (channel_id, name) 唯一键。
func channelModelNaturalKey(cm model.ChannelModel) string {
	return fmt.Sprintf("%d\x00%s", cm.ChannelID, cm.Name)
}

// usageBucketNaturalKey 组装 usage_buckets 表的 (bucket_at, model_name) 唯一键。
// 时间先按纳秒格式化, 保证与数据库时间戳比较时仅同一时刻才视为重复。
func usageBucketNaturalKey(ub model.UsageBucket) string {
	return fmt.Sprintf("%s\x00%s", ub.BucketAt.UTC().Format(time.RFC3339Nano), ub.ModelName)
}

// analyzeTableConflict 统计单表的新增/跳过行数; exists 返回 true 表示该行因主键或唯一键冲突将跳过。
func analyzeTableConflict(preview *model.DBImportPreview, table string, count int, exists func(i int) bool) {
	for i := 0; i < count; i++ {
		if exists(i) {
			preview.Summary.Skipped[table]++
		} else {
			preview.Summary.New[table]++
		}
	}
}

// detectGroupCycles 构建有效态分组引用图并检测循环。
// 有效态 = 现存分组(含其成员) + dump 新增分组(含 dump 新增成员)。
// 现存分组可被 dump 新增成员追加引用边(group_item 新 ID 指向现存 GroupID)。
func detectGroupCycles(
	existingGroups []model.Group,
	existingGroupIDs map[int]struct{},
	existingGroupItemIDs map[int]struct{},
	dump *model.DBDump,
) []string {
	// 构建分组名 → 引用分组名集合的有效态邻接表。
	refs := make(map[string]map[string]struct{})

	// 现存分组的引用边。
	for _, g := range existingGroups {
		edges := make(map[string]struct{})
		for _, item := range g.Items {
			if item.IsGroupRef() {
				edges[item.RefGroupName] = struct{}{}
			}
		}
		refs[g.Name] = edges
	}

	// dump 分组成员按 GroupID 分组。
	dumpItemsByGroupID := make(map[int][]model.GroupItem)
	for _, gi := range dump.GroupItems {
		dumpItemsByGroupID[gi.GroupID] = append(dumpItemsByGroupID[gi.GroupID], gi)
	}

	// dump 新增分组的引用边; 现存分组追加 dump 新成员的引用边。
	for _, g := range dump.Groups {
		edges, ok := refs[g.Name]
		if !ok {
			edges = make(map[string]struct{})
		}
		if _, exists := existingGroupIDs[g.ID]; !exists {
			// 新分组: 全部 dump 成员生效。
			for _, item := range dumpItemsByGroupID[g.ID] {
				if item.IsGroupRef() {
					edges[item.RefGroupName] = struct{}{}
				}
			}
		} else {
			// 现存分组: 仅 dump 新 ID 成员追加。
			for _, item := range dumpItemsByGroupID[g.ID] {
				if _, itemExists := existingGroupItemIDs[item.ID]; !itemExists && item.IsGroupRef() {
					edges[item.RefGroupName] = struct{}{}
				}
			}
		}
		refs[g.Name] = edges
	}

	// DFS 三色标记法检测环。
	var cycles []string
	const (
		white = 0 // 未访问
		gray  = 1 // 访问中(在当前 DFS 栈上)
		black = 2 // 已完成
	)
	color := make(map[string]int, len(refs))
	var stack []string

	var visit func(name string) bool
	visit = func(name string) bool {
		switch color[name] {
		case gray:
			// 找到环: 栈从该节点首次出现处到当前构成环。
			idx := 0
			for idx < len(stack) && stack[idx] != name {
				idx++
			}
			cycle := append(append([]string{}, stack[idx:]...), name)
			cycles = append(cycles, fmt.Sprintf("分组引用循环: %s", strings.Join(cycle, " → ")))
			return true
		case black:
			return false
		}
		color[name] = gray
		stack = append(stack, name)
		for ref := range refs[name] {
			if _, ok := refs[ref]; ok {
				visit(ref)
			}
		}
		stack = stack[:len(stack)-1]
		color[name] = black
		return false
	}

	// 按排序后的名称遍历, 保证输出稳定。
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if color[name] == white {
			visit(name)
		}
	}
	return cycles
}

// batchSize 控制每次 INSERT 的最大行数, 防止超过数据库绑定参数上限(SQLite/PostgreSQL 为 65535)。
const batchSize = 2000

func createDoNothing[T any](tx *gorm.DB, rows []T) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&rows, batchSize)
	return result.RowsAffected, result.Error
}

// createUpsertAll 按指定冲突列执行 upsert。保留为通用工具; 导入路径已统一为 DO NOTHING。
func createUpsertAll[T any](tx *gorm.DB, rows []T, columns []clause.Column) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	result := tx.Clauses(clause.OnConflict{
		Columns:   columns,
		UpdateAll: true,
	}).CreateInBatches(&rows, batchSize)
	return result.RowsAffected, result.Error
}

// sealSettingsForDB 加密导入设置中仍会落库的敏感列(目前是 header_templates)。
// auth_jwt_secret、proxy_url、proxy_pool 在调用前已被滤掉。
func sealSettingsForDB(rows []model.Setting) ([]model.Setting, error) {
	if len(rows) == 0 {
		return rows, nil
	}
	stored := make([]model.Setting, len(rows))
	for i, row := range rows {
		stored[i] = row
		sealed, err := sealSettingValue(row.Key, row.Value)
		if err != nil {
			return nil, err
		}
		stored[i].Value = sealed
	}
	return stored, nil
}

func createUpsertSettings(tx *gorm.DB, rows []model.Setting) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	result := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&rows)
	return result.RowsAffected, result.Error
}

// filterSecretSettings 过滤掉仅内部管理的设置项(如 JWT 密钥), 防止其随备份导出或导入覆盖
func filterSecretSettings(rows []model.Setting) []model.Setting {
	filtered := make([]model.Setting, 0, len(rows))
	for _, row := range rows {
		switch row.Key {
		case model.SettingKeyAuthJWTSecret, model.SettingKeyProxyURL, model.SettingKeyProxyPool:
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}
