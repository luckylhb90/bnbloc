package store

import (
	"context"
	"database/sql"
	"errors"
)

// MySQLPlatformSettings implements PlatformSettingsStore over database/sql.
type MySQLPlatformSettings struct{ DB *DB }

// NewMySQLPlatformSettings constructs a MySQL-backed platform settings store.
func NewMySQLPlatformSettings(db *DB) *MySQLPlatformSettings {
	return &MySQLPlatformSettings{DB: db}
}

var _ PlatformSettingsStore = (*MySQLPlatformSettings)(nil)

func (s *MySQLPlatformSettings) PutPlatformSetting(ctx context.Context, setting PlatformSetting) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO platform_settings
  (setting_key, setting_value, updated_at_ms, updated_by)
  VALUES (?, ?, ?, ?)
  ON DUPLICATE KEY UPDATE
    setting_value = VALUES(setting_value),
    updated_at_ms = VALUES(updated_at_ms),
    updated_by = VALUES(updated_by)`, setting.Key, setting.Value, setting.UpdatedAtMs, setting.UpdatedBy)
	return err
}

func (s *MySQLPlatformSettings) GetPlatformSetting(ctx context.Context, key string) (PlatformSetting, bool, error) {
	var setting PlatformSetting
	err := s.DB.QueryRowContext(ctx, `SELECT setting_key, setting_value, updated_at_ms, updated_by FROM platform_settings WHERE setting_key = ?`, key).Scan(
		&setting.Key, &setting.Value, &setting.UpdatedAtMs, &setting.UpdatedBy,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PlatformSetting{}, false, nil
	}
	if err != nil {
		return PlatformSetting{}, false, err
	}
	return setting, true, nil
}

func (s *MySQLPlatformSettings) ListPlatformSettings(ctx context.Context) ([]PlatformSetting, error) {
	return queryAll(ctx, s.DB, `SELECT setting_key, setting_value, updated_at_ms, updated_by FROM platform_settings ORDER BY setting_key`,
		func(r rowScanner) (PlatformSetting, error) {
			var setting PlatformSetting
			err := r.Scan(&setting.Key, &setting.Value, &setting.UpdatedAtMs, &setting.UpdatedBy)
			return setting, err
		})
}

// SwapPlatformSetting is an INSERT for an unset key (a concurrent insert is
// the duplicate key) and otherwise one conditional UPDATE on the row as
// read: the value compares byte-exact (the column's collation would fold
// case), the time and author exactly.
func (s *MySQLPlatformSettings) SwapPlatformSetting(ctx context.Context, prev *PlatformSetting, next PlatformSetting) error {
	if prev == nil {
		_, err := s.DB.ExecContext(ctx, `INSERT INTO platform_settings (setting_key, setting_value, updated_at_ms, updated_by)
  VALUES (?, ?, ?, ?)`, next.Key, next.Value, next.UpdatedAtMs, next.UpdatedBy)
		return dupAs(err, ErrPlatformSettingChanged)
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE platform_settings SET setting_value = ?, updated_at_ms = ?, updated_by = ?
  WHERE setting_key = ? AND CAST(setting_value AS BINARY) = CAST(? AS BINARY) AND updated_at_ms = ? AND updated_by = ?`,
		next.Value, next.UpdatedAtMs, next.UpdatedBy, next.Key, prev.Value, prev.UpdatedAtMs, prev.UpdatedBy)
	if err != nil {
		return err
	}
	return requireMatched(res, ErrPlatformSettingChanged)
}

func (s *MySQLPlatformSettings) DeletePlatformSetting(ctx context.Context, key string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM platform_settings WHERE setting_key = ?`, key)
	return err
}
