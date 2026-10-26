package contractv1

import (
	"fmt"
	"regexp"
	"strings"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
)

var catalogImageID = regexp.MustCompile(`^img_[0-9a-f]{32}$`)

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
		if !catalogImageID.MatchString(engine.GetImageId()) {
			return fmt.Errorf("image_id must be a Resource catalog image ID")
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
