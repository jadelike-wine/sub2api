package repository

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/imageconversation"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type imageConversationRepository struct {
	client *ent.Client
}

// NewImageConversationRepository 构造图片会话仓库。
// 所有查询强制附带 user_id 条件，避免跨用户越权访问。
func NewImageConversationRepository(client *ent.Client) service.ImageConversationRepository {
	return &imageConversationRepository{client: client}
}

func (r *imageConversationRepository) Create(ctx context.Context, params service.CreateImageConversationParams) (*service.ImageConversation, error) {
	title := strings.TrimSpace(params.Title)
	b := r.client.ImageConversation.Create().
		SetUserID(params.UserID).
		SetTitle(title)
	m, err := b.Save(ctx)
	if err != nil {
		return nil, err
	}
	return conversationToService(m), nil
}

func (r *imageConversationRepository) GetByIDForOwner(ctx context.Context, userID, id int64) (*service.ImageConversation, error) {
	m, err := r.client.ImageConversation.Query().
		Where(
			imageconversation.IDEQ(id),
			imageconversation.UserIDEQ(userID),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, service.ErrImageConversationNotFound
		}
		return nil, err
	}
	return conversationToService(m), nil
}

func (r *imageConversationRepository) List(ctx context.Context, filter service.ImageConversationFilter) (*service.ImageConversationList, error) {
	page := filter.Page
	if page <= 0 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	q := r.client.ImageConversation.Query().Where(imageconversation.UserIDEQ(filter.UserID))
	if kw := strings.TrimSpace(filter.Keyword); kw != "" {
		q = q.Where(imageconversation.TitleContainsFold(kw))
	}
	if filter.CreatedAfter != nil {
		q = q.Where(imageconversation.CreatedAtGTE(*filter.CreatedAfter))
	}
	if filter.CreatedBefore != nil {
		q = q.Where(imageconversation.CreatedAtLTE(*filter.CreatedBefore))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, err
	}

	items, err := q.
		Order(ent.Desc(imageconversation.FieldLastMessageAt), ent.Desc(imageconversation.FieldID)).
		Offset(offset).
		Limit(pageSize).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*service.ImageConversation, 0, len(items))
	for _, m := range items {
		out = append(out, conversationToService(m))
	}
	return &service.ImageConversationList{
		Items:    out,
		Total:    int64(total),
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func (r *imageConversationRepository) Update(ctx context.Context, userID, id int64, params service.UpdateImageConversationParams) (*service.ImageConversation, error) {
	b := r.client.ImageConversation.Update().
		Where(
			imageconversation.IDEQ(id),
			imageconversation.UserIDEQ(userID),
		)
	if params.Title != nil {
		b.SetTitle(strings.TrimSpace(*params.Title))
	}
	affected, err := b.Save(ctx)
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, service.ErrImageConversationNotFound
	}
	// 更新成功后重新查询实体返回（附带 user_id 隔离）
	return r.GetByIDForOwner(ctx, userID, id)
}

func (r *imageConversationRepository) TouchLastMessageAt(ctx context.Context, userID, id int64, at time.Time) error {
	affected, err := r.client.ImageConversation.Update().
		Where(
			imageconversation.IDEQ(id),
			imageconversation.UserIDEQ(userID),
		).
		SetLastMessageAt(at).
		Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrImageConversationNotFound
	}
	return nil
}

func (r *imageConversationRepository) SoftDelete(ctx context.Context, userID, id int64) error {
	// SoftDeleteMixin 的 Hook 会把 Delete 转成 UPDATE deleted_at = NOW()
	rows, err := r.client.ImageConversation.Delete().
		Where(
			imageconversation.IDEQ(id),
			imageconversation.UserIDEQ(userID),
		).
		Exec(ctx)
	if err != nil {
		return err
	}
	if rows == 0 {
		return service.ErrImageConversationNotFound
	}
	return nil
}

// conversationToService 把 ent 模型映射到 service 层。
func conversationToService(m *ent.ImageConversation) *service.ImageConversation {
	return &service.ImageConversation{
		ID:            m.ID,
		UserID:        m.UserID,
		Title:         m.Title,
		LastMessageAt: m.LastMessageAt,
		CreatedAt:     m.CreatedAt,
		UpdatedAt:     m.UpdatedAt,
		DeletedAt:     m.DeletedAt,
	}
}
