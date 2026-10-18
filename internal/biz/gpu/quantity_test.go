package gpu

import "testing"

func TestExecutionResourcesRemainOpaque(t *testing.T) {
	s := validSnapshot()
	s.Execution.Resources["vendor.com/gpu-memory"] = "8192"
	if err := ValidateSnapshot(s, s.Request); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionValidationRejectsEmptyResourceEntries(t *testing.T) {
	s := validSnapshot()
	s.Execution.Resources["vendor.com/gpu-memory"] = ""
	if err := ValidateExecution(s.Execution); err == nil {
		t.Fatal("empty execution resource accepted")
	}
}

func TestExecutionDigestMustMatchSnapshot(t *testing.T) {
	s := validSnapshot()
	s.ExecutionDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := ValidateSnapshot(s, s.Request); err == nil {
		t.Fatal("snapshot with mismatched execution digest accepted")
	}
}
