package migrate

// 清理 029 后补回的 V1 渠道。
func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 32,
		Up:      deleteRetiredOpencodeFreeV1Channels,
	})
}
