package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/testutil"
	"github.com/spf13/cobra"
)

func setupIssueKeySettingsRepo(t *testing.T, settingsJSON string) {
	t.Helper()
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	testutil.WriteFile(t, dir, ".entire/settings.json", settingsJSON)
	t.Chdir(dir)
}

func runDoctorIssueKeyCheck(t *testing.T) string {
	t.Helper()
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetContext(context.Background())
	checkIssueKeyPattern(cmd)
	return out.String()
}

// Uses t.Chdir — do NOT add t.Parallel().
func TestIssueKeyPattern_InvalidReportedByDoctorAndStatus(t *testing.T) {
	setupIssueKeySettingsRepo(t, `{"enabled": true, "issue_key_pattern": "[A-Z\\d+"}`)

	// The loader accepts it: hooks keep working.
	if _, err := LoadEntireSettings(context.Background()); err != nil {
		t.Fatalf("LoadEntireSettings() error = %v, want an invalid pattern to load", err)
	}

	doctor := runDoctorIssueKeyCheck(t)
	if !strings.Contains(doctor, "Issue key pattern: INVALID") || !strings.Contains(doctor, "`[A-Z\\d+`") {
		t.Fatalf("doctor output = %q, want an INVALID report quoting the pattern", doctor)
	}

	for _, detailed := range []bool{false, true} {
		var stdout bytes.Buffer
		err := runStatus(context.Background(), &stdout, detailed, false)
		if err == nil || !strings.Contains(err.Error(), "invalid issue_key_pattern") {
			t.Fatalf("runStatus(detailed=%v) error = %v, want the pattern error", detailed, err)
		}
	}
}

// Uses t.Chdir — do NOT add t.Parallel().
func TestIssueKeyPattern_ValidIsQuietInDoctor(t *testing.T) {
	setupIssueKeySettingsRepo(t, `{"enabled": true, "issue_key_pattern": "[A-Z]+-\\d+"}`)

	if out := runDoctorIssueKeyCheck(t); out != "" {
		t.Fatalf("doctor output = %q, want nothing for a valid pattern", out)
	}
	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, false, false); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}
}
