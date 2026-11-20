package database

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// The fork used 106–112 before upstream released 106–109. Move only the
// recognized, contiguous fork ledger, preserving checksums and applied times.
func relocateExternalMCP106Migrations(tx *gorm.DB) error {
	var first schemaMigration
	if err := tx.First(&first, "version = ?", 106).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if first.Name != "external_mcp_access" {
		return nil
	}
	var rows []schemaMigration
	if err := tx.Where("version >= ?", 106).Order("version").Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) > 7 {
		return fmt.Errorf("本地 MCP 迁移账本超出已知范围，拒绝转换")
	}
	tables := [][]string{
		{"user_api_keys", "mcp_clients", "mcp_authorization_codes", "tasks", "projects"},
		{"projects"},
		{"drama_segment_prompt_revisions"},
		{"drama_mutation_receipts", "drama_shots", "drama_segments", "drama_takes"},
		{"drama_audio_versions", "drama_audio_resource_links"},
		{"drama_audio_checks"},
		{"drama_shot_media_reviews"},
	}
	for i, row := range rows {
		oldVersion := int64(106 + i)
		if row.Version != oldVersion {
			return fmt.Errorf("本地 MCP 迁移账本不连续：缺少 %d", oldVersion)
		}
		var expected migration
		for _, item := range schemaMigrations {
			if item.version == oldVersion+4 {
				expected = item
				break
			}
		}
		if row.Name != expected.name || row.Checksum != expected.checksum {
			return fmt.Errorf("本地 MCP 迁移 %d 不匹配", oldVersion)
		}
		for _, table := range tables[i] {
			if !tx.Migrator().HasTable(table) {
				return fmt.Errorf("本地 MCP 迁移表缺失：%s", table)
			}
		}
		if i == 1 && !tx.Migrator().HasColumn("projects", "external_request_hash") {
			return errors.New("本地 MCP 迁移字段缺失：projects.external_request_hash")
		}
	}
	// Remove the validated source rows before inserting destinations: 110–112
	// overlap source versions. MigrateSchema owns the enclosing transaction.
	for _, row := range rows {
		result := tx.Where("version = ? AND name = ? AND checksum = ?", row.Version, row.Name, row.Checksum).Delete(&schemaMigration{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("本地 MCP 迁移 %d 转换失败", row.Version)
		}
	}
	for _, row := range rows {
		row.Version += 4
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}
