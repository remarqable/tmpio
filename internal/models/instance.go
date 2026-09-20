package models

import (
	"context"
	goerrors "errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/errors"
)

// InstanceSetting is the one row of operator-owned configuration. It is
// instance-wide on purpose: the model key pays for every organization's calls,
// so it belongs to whoever runs the server, not to any tenant. Whether a given
// organization's content may be sent is a separate, per-tenant decision.
type InstanceSetting struct {
	ID            int16 `gorm:"primaryKey"`
	AIAPIKey      string
	AIModel       string
	AIBaseURL     string
	AIWorkspaceID string
	CreatedAt     time.Time `gorm:"autoCreateTime"`
	UpdatedAt     time.Time `gorm:"autoUpdateTime"`
}

// TableName follows the singular naming convention.
func (InstanceSetting) TableName() string { return "instance_setting" }

// GetInstanceSetting reads the singleton row, creating it if a database
// predates the migration that seeds it.
func GetInstanceSetting(ctx context.Context) (*InstanceSetting, error) {
	var s InstanceSetting
	err := db.Get().WithContext(ctx).Where("id = 1").First(&s).Error
	if goerrors.Is(err, gorm.ErrRecordNotFound) {
		return &InstanceSetting{ID: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// SaveInstanceAI records the operator's model credentials. An empty key turns
// AI-assisted filing off for the whole installation, whatever each tenant has
// chosen.
func SaveInstanceAI(ctx context.Context, apiKey, model, baseURL, workspaceID string) error {
	apiKey = strings.TrimSpace(apiKey)
	model = strings.TrimSpace(model)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	workspaceID = strings.TrimSpace(workspaceID)
	if baseURL != "" && !strings.HasPrefix(baseURL, "https://") && !strings.HasPrefix(baseURL, "http://") {
		return errors.New(errors.CodeValidationFailed, "the API base URL must be an http:// or https:// URL")
	}
	return db.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO instance_setting (id, ai_api_key, ai_model, ai_base_url, ai_workspace_id)
			VALUES (1, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET
				ai_api_key = EXCLUDED.ai_api_key,
				ai_model = EXCLUDED.ai_model,
				ai_base_url = EXCLUDED.ai_base_url,
				ai_workspace_id = EXCLUDED.ai_workspace_id`,
			apiKey, model, baseURL, workspaceID).Error
	})
}

// SetInstanceAdmin marks the account that administers the installation. The
// setup wizard calls this for the account it creates.
func SetInstanceAdmin(ctx context.Context, userID int64) error {
	return db.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Model(&User{}).Where("id = ?", userID).Update("is_instance_admin", true).Error
	})
}

// InstanceHasOwner reports whether anyone has claimed this installation yet.
// While it is false the setup flow is open; once true it is closed for good.
func InstanceHasOwner(ctx context.Context) (bool, error) {
	var n int64
	err := db.Get().WithContext(ctx).Model(&User{}).Count(&n).Error
	return n > 0, err
}

// InstanceAI is the operator's model configuration.
type InstanceAI struct {
	APIKey      string
	Model       string
	BaseURL     string
	WorkspaceID string
}

// SeedInstanceAIFromEnv stores the environment's key when nothing is stored
// yet, and reports whether it did. It never overwrites a key set through the
// settings page: the environment is a starting point, not an authority, or
// changing the key in the product would last only until the next restart.
func SeedInstanceAIFromEnv(ctx context.Context, in InstanceAI) (bool, error) {
	if in.APIKey == "" {
		return false, nil
	}
	current, err := GetInstanceSetting(ctx)
	if err != nil {
		return false, err
	}
	if current.AIAPIKey != "" {
		return false, nil
	}
	return true, SaveInstanceAI(ctx, in.APIKey, in.Model, in.BaseURL, in.WorkspaceID)
}
