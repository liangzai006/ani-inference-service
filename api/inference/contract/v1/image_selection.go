package contractv1

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
)

// ValidateEngineSelection keeps historical raw commands readable while making
// catalog selection explicit and mutually exclusive with a caller image ref.
func ValidateEngineSelection(engine *inferencev1.EngineSpec, version uint32) error {
	if engine == nil || strings.TrimSpace(engine.GetType()) == "" || strings.TrimSpace(engine.GetCommand()) == "" {
		return fmt.Errorf("engine type and complete command are required")
	}
	switch version {
	case 0:
		if engine.GetImageId() != "" || engine.GetImageScope() != "" || strings.TrimSpace(engine.GetImage()) == "" {
			return fmt.Errorf("legacy engine requires raw image and no catalog selection")
		}
	case 2:
		id, err := uuid.Parse(engine.GetImageId())
		if err != nil || id == uuid.Nil || id.String() != engine.GetImageId() {
			return fmt.Errorf("image_id must be a canonical UUID")
		}
		if engine.GetImageScope() != "platform" && engine.GetImageScope() != "tenant" {
			return fmt.Errorf("image_scope must be platform or tenant")
		}
		if engine.GetImage() != "" {
			return fmt.Errorf("catalog selection and raw image are mutually exclusive")
		}
	default:
		return fmt.Errorf("unsupported command contract version")
	}
	return nil
}
