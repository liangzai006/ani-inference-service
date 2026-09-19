package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgtype"
)

func (u *ReadUseCase) HasActiveModelVersionReferences(ctx context.Context, tenant string, versions []string) (bool, error) {
	if u == nil || u.pool == nil {
		return false, errors.New("reference store not configured")
	}
	t, err := tenantUUID(tenant)
	if err != nil {
		return false, err
	}
	if len(versions) < 1 || len(versions) > 256 {
		return false, errors.New("expected 1 to 256 model version IDs")
	}
	ids := make([]pgtype.UUID, len(versions))
	for i, v := range versions {
		id, e := workUUID("model_version_id", v)
		if e != nil {
			return false, e
		}
		ids[i] = id
	}
	return New(u.pool).HasActiveModelVersionReferences(ctx, HasActiveModelVersionReferencesParams{TenantID: t, Column2: ids})
}
