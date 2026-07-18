package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/imageprovidercredential"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type imageCredentialRepository struct {
	client *ent.Client
}

// NewImageCredentialRepository 构造 Agnes 凭据仓库。
func NewImageCredentialRepository(client *ent.Client) service.ImageCredentialRepository {
	return &imageCredentialRepository{client: client}
}

func (r *imageCredentialRepository) ListSchedulable(ctx context.Context, provider string) ([]*service.ImageProviderCredential, error) {
	now := time.Now()
	q := r.client.ImageProviderCredential.Query().
		Where(
			imageprovidercredential.ProviderEQ(imageprovidercredential.Provider(provider)),
			imageprovidercredential.EnabledEQ(true),
			imageprovidercredential.StatusNEQ(imageprovidercredential.StatusDisabled),
			imageprovidercredential.Or(
				imageprovidercredential.CooldownUntilIsNil(),
				imageprovidercredential.CooldownUntilLTE(now),
			),
		).
		Order(ent.Asc(imageprovidercredential.FieldPriority), ent.Asc(imageprovidercredential.FieldID))
	ms, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*service.ImageProviderCredential, 0, len(ms))
	for _, m := range ms {
		out = append(out, credentialToService(m))
	}
	return out, nil
}

func (r *imageCredentialRepository) ListAll(ctx context.Context) ([]*service.ImageProviderCredential, error) {
	ms, err := r.client.ImageProviderCredential.Query().
		Order(ent.Asc(imageprovidercredential.FieldPriority), ent.Desc(imageprovidercredential.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*service.ImageProviderCredential, 0, len(ms))
	for _, m := range ms {
		out = append(out, credentialToService(m))
	}
	return out, nil
}

func (r *imageCredentialRepository) GetByID(ctx context.Context, id int64) (*service.ImageProviderCredential, error) {
	m, err := r.client.ImageProviderCredential.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, service.ErrImageCredentialNotFound
		}
		return nil, err
	}
	return credentialToService(m), nil
}

func (r *imageCredentialRepository) Create(ctx context.Context, c *service.ImageProviderCredential) (*service.ImageProviderCredential, error) {
	b := r.client.ImageProviderCredential.Create().
		SetName(c.Name).
		SetProvider(imageprovidercredential.Provider(c.Provider)).
		SetAPIKeyEncrypted(c.ApiKeyEncrypted).
		SetKeyFingerprint(c.KeyFingerprint).
		SetEnabled(c.Enabled).
		SetPriority(c.Priority).
		SetWeight(c.Weight).
		SetStatus(imageprovidercredential.Status(c.Status))
	m, err := b.Save(ctx)
	if err != nil {
		return nil, err
	}
	return credentialToService(m), nil
}

func (r *imageCredentialRepository) Update(ctx context.Context, c *service.ImageProviderCredential) (*service.ImageProviderCredential, error) {
	b := r.client.ImageProviderCredential.UpdateOneID(c.ID).
		SetName(c.Name).
		SetAPIKeyEncrypted(c.ApiKeyEncrypted).
		SetKeyFingerprint(c.KeyFingerprint).
		SetEnabled(c.Enabled).
		SetPriority(c.Priority).
		SetWeight(c.Weight).
		SetStatus(imageprovidercredential.Status(c.Status))
	m, err := b.Save(ctx)
	if err != nil {
		return nil, err
	}
	return credentialToService(m), nil
}

func (r *imageCredentialRepository) Delete(ctx context.Context, id int64) error {
	return r.client.ImageProviderCredential.DeleteOneID(id).Exec(ctx)
}

func (r *imageCredentialRepository) UpdateHealth(ctx context.Context, id int64, update service.CredentialHealthUpdate) error {
	b := r.client.ImageProviderCredential.UpdateOneID(id)
	if update.Status != nil {
		b.SetStatus(imageprovidercredential.Status(*update.Status))
	}
	if update.ConsecutiveFailures != nil {
		b.SetConsecutiveFailures(*update.ConsecutiveFailures)
	}
	if update.LastUsedAt != nil {
		b.SetLastUsedAt(*update.LastUsedAt)
	}
	if update.LastSuccessAt != nil {
		b.SetLastSuccessAt(*update.LastSuccessAt)
	}
	if update.LastFailureAt != nil {
		b.SetLastFailureAt(*update.LastFailureAt)
	}
	if update.CooldownUntil != nil {
		b.SetCooldownUntil(*update.CooldownUntil)
	} else if update.CooldownUntil == nil && update.Status != nil && *update.Status == service.ImageCredentialStatusHealthy {
		// 恢复健康时清空冷却
		b.ClearCooldownUntil()
	}
	if update.LastErrorCode != nil {
		b.SetLastErrorCode(*update.LastErrorCode)
	}
	if update.LastErrorMessage != nil {
		b.SetLastErrorMessage(*update.LastErrorMessage)
	}
	return b.Exec(ctx)
}

func credentialToService(m *ent.ImageProviderCredential) *service.ImageProviderCredential {
	return &service.ImageProviderCredential{
		ID:                  m.ID,
		Name:                m.Name,
		Provider:            string(m.Provider),
		ApiKeyEncrypted:     m.APIKeyEncrypted,
		KeyFingerprint:      m.KeyFingerprint,
		Enabled:             m.Enabled,
		Priority:            m.Priority,
		Weight:              m.Weight,
		Status:              string(m.Status),
		ConsecutiveFailures: m.ConsecutiveFailures,
		LastUsedAt:          m.LastUsedAt,
		LastSuccessAt:       m.LastSuccessAt,
		LastFailureAt:       m.LastFailureAt,
		CooldownUntil:       m.CooldownUntil,
		LastErrorCode:       m.LastErrorCode,
		LastErrorMessage:    m.LastErrorMessage,
		CreatedAt:           m.CreatedAt,
		UpdatedAt:           m.UpdatedAt,
	}
}
