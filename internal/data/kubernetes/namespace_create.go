package kubernetes

import (
	"context"
	"errors"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	biz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
)

// NamespaceCreate resolves platform ownership before image admission and
// commits the resolved identity as part of service acceptance.
type NamespaceCreate struct {
	Resolver interface {
		Resolve(context.Context, string) (TenantNamespace, error)
	}
	Preflight     biz.CreatePreflight
	Next          biz.CreateUseCase
	Prerequisites interface {
		Check(context.Context, TenantNamespace) error
	}
}

func (u *NamespaceCreate) Create(ctx context.Context, in biz.CreateInput) (*inferencev1.OperationResponse, error) {
	if u == nil || u.Resolver == nil || u.Preflight == nil || u.Next == nil {
		return nil, errors.New("tenant namespace creation dependencies are required")
	}
	if out, handled, err := u.Preflight.PrepareCreate(ctx, in); handled || err != nil {
		return out, err
	}
	ns, err := u.Resolver.Resolve(ctx, in.TenantID)
	if err != nil {
		if out, handled, replayErr := u.Preflight.PrepareCreate(ctx, in); handled || replayErr != nil {
			return out, replayErr
		}
		return nil, err
	}
	if u.Prerequisites != nil {
		if err := u.Prerequisites.Check(ctx, ns); err != nil {
			if out, handled, replayErr := u.Preflight.PrepareCreate(ctx, in); handled || replayErr != nil {
				return out, replayErr
			}
			return nil, err
		}
	}
	in.RuntimeNamespace, in.RuntimeNamespaceUID = ns.Name, ns.UID
	return u.Next.Create(ctx, in)
}
