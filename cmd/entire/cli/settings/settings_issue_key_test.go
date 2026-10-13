package settings

import (
	"context"
	"strings"
	"testing"
)

func TestLoad_IssueKeyPattern(t *testing.T) {
	setupSettingsDir(t, `{"enabled": true, "issue_key_pattern": "[A-Z][A-Z0-9]+-\\d+"}`, "")

	s, err := Load(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.IssueKeyPattern != `[A-Z][A-Z0-9]+-\d+` {
		t.Fatalf("IssueKeyPattern = %q", s.IssueKeyPattern)
	}
	re, err := s.IssueKeyRegexp()
	if err != nil || re == nil {
		t.Fatalf("IssueKeyRegexp() = (%v, %v), want compiled pattern", re, err)
	}
	if got := re.FindString("feat/PROJ-123-login"); got != "PROJ-123" {
		t.Errorf("FindString = %q, want PROJ-123", got)
	}
}

func TestLoad_IssueKeyPatternDefaultsOff(t *testing.T) {
	setupSettingsDir(t, `{"enabled": true}`, "")

	s, err := Load(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if re, reErr := s.IssueKeyRegexp(); s.IssueKeyPattern != "" || re != nil || reErr != nil {
		t.Fatalf("expected issue linking off by default, got pattern %q (%v)", s.IssueKeyPattern, reErr)
	}
}

// An invalid pattern must not fail the load: every Entire hook loads settings,
// so a typo in the shared file would otherwise turn all of them off.
func TestLoad_IssueKeyPatternInvalidDisablesOnlyIssueLinking(t *testing.T) {
	setupSettingsDir(t, `{"enabled": true, "commit_linking": "always", "issue_key_pattern": "[A-Z\\d+"}`, "")

	s, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v, want an invalid pattern to load", err)
	}
	if !s.Enabled || s.GetCommitLinking() != CommitLinkingAlways {
		t.Fatalf("other settings must still load: %+v", s)
	}
	re, reErr := s.IssueKeyRegexp()
	if re != nil || reErr == nil {
		t.Fatalf("IssueKeyRegexp() = (%v, %v), want (nil, error)", re, reErr)
	}
	// The pattern is quoted as written, not Go-escaped (no doubled backslash).
	if !strings.Contains(reErr.Error(), "invalid issue_key_pattern `[A-Z\\d+`") {
		t.Fatalf("unexpected error text: %v", reErr)
	}
}

func TestLoad_IssueKeyPatternLocalOverride(t *testing.T) {
	setupSettingsDir(t, `{"enabled": true, "issue_key_pattern": "ENG-\\d+"}`, `{"issue_key_pattern": "PROJ-\\d+"}`)

	s, err := Load(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.IssueKeyPattern != `PROJ-\d+` {
		t.Fatalf("IssueKeyPattern = %q, want local override", s.IssueKeyPattern)
	}
}

func TestLoad_IssueKeyPatternLocalEmptyTurnsOff(t *testing.T) {
	setupSettingsDir(t, `{"enabled": true, "issue_key_pattern": "ENG-\\d+"}`, `{"issue_key_pattern": ""}`)

	s, err := Load(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if re, reErr := s.IssueKeyRegexp(); s.IssueKeyPattern != "" || re != nil || reErr != nil {
		t.Fatalf("IssueKeyPattern = %q, want a local \"\" to turn the project pattern off", s.IssueKeyPattern)
	}
}

func TestLoad_IssueKeyPatternLocalAbsentKeepsProject(t *testing.T) {
	setupSettingsDir(t, `{"enabled": true, "issue_key_pattern": "ENG-\\d+"}`, `{"commit_linking": "always"}`)

	s, err := Load(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.IssueKeyPattern != `ENG-\d+` {
		t.Fatalf("IssueKeyPattern = %q, want the project pattern", s.IssueKeyPattern)
	}
}

func TestLoadFromBytes_IssueKeyPatternInvalidLoads(t *testing.T) {
	t.Parallel()

	s, err := LoadFromBytes([]byte(`{"issue_key_pattern": "*"}`))
	if err != nil {
		t.Fatalf("LoadFromBytes() error = %v", err)
	}
	if _, reErr := s.IssueKeyRegexp(); reErr == nil {
		t.Fatal("IssueKeyRegexp() must report the invalid pattern")
	}
}

func TestIssueKeyRegexp_Nil(t *testing.T) {
	t.Parallel()

	var nilSettings *EntireSettings
	if re, err := nilSettings.IssueKeyRegexp(); re != nil || err != nil {
		t.Error("nil settings must report no pattern and no error")
	}
}
