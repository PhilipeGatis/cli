package trailers

import "testing"

func TestParseIssue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		msg    string
		want   string
		wantOK bool
	}{
		{name: "trailer present", msg: "feat: x\n\nEntire-Checkpoint: abc123def456\nEntire-Issue: PROJ-123\n", want: "PROJ-123", wantOK: true},
		{name: "no space after colon", msg: "feat: x\n\nEntire-Issue:ENG-42\n", want: "ENG-42", wantOK: true},
		{name: "key is case-insensitive", msg: "feat: x\n\nentire-issue: ENG-42\n", want: "ENG-42", wantOK: true},
		{name: "first of several", msg: "feat: x\n\nEntire-Issue: A-1\nEntire-Issue: B-2\n", want: "A-1", wantOK: true},
		{name: "mixed with other trailers", msg: "feat: x\n\nSigned-off-by: A <a@b.c>\nEntire-Issue: A-1\n", want: "A-1", wantOK: true},
		{name: "continuation line in block", msg: "feat: x\n\nCo-authored-by: A\n  <a@b.c>\nEntire-Issue: A-1\n", want: "A-1", wantOK: true},
		{name: "CRLF", msg: "feat: x\r\n\r\nEntire-Checkpoint: abc123def456\r\nEntire-Issue: A-1\r\n", want: "A-1", wantOK: true},
		{name: "absent", msg: "feat: x\n\nEntire-Checkpoint: abc123def456\n", wantOK: false},
		{name: "indented line is not a trailer", msg: "feat: x\n\n    Entire-Issue: ENG-42\n", wantOK: false},
		{name: "body paragraph is not the trailer block", msg: "feat: x\n\nEntire-Issue: OLD-1\n\nmore prose\n", wantOK: false},
		{name: "mixed final paragraph is not a trailer block", msg: "feat: x\n\nsome prose\nEntire-Issue: A-1\n", wantOK: false},
		{name: "subject paragraph is never a trailer block", msg: "Entire-Issue: A-1\n", wantOK: false},
		{name: "prose mention is not a trailer", msg: "fix Entire-Issue: PROJ-1 handling\n", wantOK: false},
		{name: "value with spaces is not a key", msg: "feat: x\n\nEntire-Issue: PROJ 1\n", wantOK: false},
		{name: "empty value", msg: "feat: x\n\nEntire-Issue: \n", wantOK: false},
		{name: "below scissors ignored", msg: "feat: x\n\nSigned-off-by: A\n# ------------------------ >8 ------------------------\nEntire-Issue: PROJ-1\n", wantOK: false},
		{
			name: "squash message: quoted trailers in the body do not count",
			msg: "Squashed commit of the following:\n\ncommit 1111111\nAuthor: A <a@b.c>\n\n    old work\n\n" +
				"    Entire-Checkpoint: aaaaaaaaaaaa\n    Entire-Issue: OLD-1\n\n" +
				"Entire-Checkpoint: bbbbbbbbbbbb\nEntire-Issue: NEW-2\n",
			want: "NEW-2", wantOK: true,
		},
		{
			name: "squash message: own trailers appended to the quoted paragraph",
			msg: "Squash\n\nSquashed commit of the following:\n\n    old work\n\n" +
				"    Entire-Issue: OLD-1\nEntire-Checkpoint: bbbbbbbbbbbb\nEntire-Issue: NEW-2\n",
			want: "NEW-2", wantOK: true,
		},
		{
			name: "squash message without its own key",
			msg: "Squashed commit of the following:\n\ncommit 1111111\nAuthor: A <a@b.c>\n\n    old work\n\n" +
				"    Entire-Checkpoint: aaaaaaaaaaaa\n    Entire-Issue: OLD-1\n",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ParseIssue(tt.msg)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("ParseIssue() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestParseDraftIssue_IgnoresCommentLines(t *testing.T) {
	t.Parallel()

	editor := "fix login\n\nEntire-Checkpoint: abc123def456\nEntire-Issue: PROJ-1\n# Remove the trailer above\n\n# Please enter the commit message\n"
	if got, ok := ParseDraftIssue(editor, "#"); !ok || got != "PROJ-1" {
		t.Errorf("ParseDraftIssue(#) = (%q, %v), want PROJ-1", got, ok)
	}
	if _, ok := ParseIssue(editor); ok {
		t.Error("ParseIssue must not skip comment lines: the last paragraph is git's comment block")
	}

	semicolon := "fix login\n\nEntire-Checkpoint: abc123def456\nEntire-Issue: PROJ-1\n\n; Please enter the commit message\n"
	if got, ok := ParseDraftIssue(semicolon, ";"); !ok || got != "PROJ-1" {
		t.Errorf("ParseDraftIssue(;) = (%q, %v), want PROJ-1", got, ok)
	}
}

func TestAppendIssueTrailer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  string
		key  string
		want string
	}{
		{
			name: "after checkpoint trailer",
			msg:  "feat: login\n\nEntire-Checkpoint: abc123def456\n",
			key:  "PROJ-123",
			want: "feat: login\n\nEntire-Checkpoint: abc123def456\nEntire-Issue: PROJ-123\n",
		},
		{
			name: "after checkpoint trailer, before git comments",
			msg:  "feat: login\n\nEntire-Checkpoint: abc123def456\n\n# Please enter the commit message\n",
			key:  "PROJ-123",
			want: "feat: login\n\nEntire-Checkpoint: abc123def456\nEntire-Issue: PROJ-123\n\n# Please enter the commit message\n",
		},
		{
			name: "above scissors",
			msg:  "feat: login\n\nEntire-Checkpoint: abc123def456\n# ------------------------ >8 ------------------------\ndiff --git a b\nEntire-Checkpoint: 111111111111\n",
			key:  "ENG-42",
			want: "feat: login\n\nEntire-Checkpoint: abc123def456\nEntire-Issue: ENG-42\n# ------------------------ >8 ------------------------\ndiff --git a b\nEntire-Checkpoint: 111111111111\n",
		},
		{
			name: "after the last of several checkpoint trailers",
			msg:  "squash\n\nEntire-Checkpoint: aaaaaaaaaaaa\nEntire-Checkpoint: bbbbbbbbbbbb\nSigned-off-by: A <a@b.c>\n",
			key:  "ENG-42",
			want: "squash\n\nEntire-Checkpoint: aaaaaaaaaaaa\nEntire-Checkpoint: bbbbbbbbbbbb\nEntire-Issue: ENG-42\nSigned-off-by: A <a@b.c>\n",
		},
		{
			name: "indented checkpoint lines in a squash body are skipped",
			msg:  "squash\n\n    Entire-Checkpoint: aaaaaaaaaaaa\n",
			key:  "ENG-42",
			want: "squash\n\n    Entire-Checkpoint: aaaaaaaaaaaa\n\nEntire-Issue: ENG-42\n",
		},
		{
			name: "CRLF message keeps CRLF",
			msg:  "feat: login\r\n\r\nEntire-Checkpoint: abc123def456\r\n",
			key:  "PROJ-1",
			want: "feat: login\r\n\r\nEntire-Checkpoint: abc123def456\r\nEntire-Issue: PROJ-1\r\n",
		},
		{
			name: "CRLF message without checkpoint trailer",
			msg:  "feat: login\r\n",
			key:  "PROJ-1",
			want: "feat: login\r\n\r\nEntire-Issue: PROJ-1\r\n",
		},
		{
			name: "no checkpoint trailer appends a trailer block",
			msg:  "feat: login\n",
			key:  "PROJ-1",
			want: "feat: login\n\nEntire-Issue: PROJ-1\n",
		},
		{
			name: "empty key is a no-op",
			msg:  "feat: login\n",
			key:  "",
			want: "feat: login\n",
		},
		{
			name: "whitespace key is a no-op",
			msg:  "feat: login\n",
			key:  "PROJ 1",
			want: "feat: login\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := AppendIssueTrailer(tt.msg, tt.key)
			if got != tt.want {
				t.Errorf("AppendIssueTrailer() = %q, want %q", got, tt.want)
			}
			if got != tt.msg {
				if key, ok := ParseDraftIssue(got, "#"); !ok || key != tt.key {
					t.Errorf("ParseDraftIssue(result) = (%q, %v), want %q", key, ok, tt.key)
				}
			}
		})
	}
}

func TestHasCheckpointTrailerLine(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"x\n\nEntire-Checkpoint: abc123def456\n":                                                       true,
		"x\n\nEntire-Checkpoint: abc123def456\r\n":                                                     true,
		"x\n\n    Entire-Checkpoint: abc123def456\n":                                                   false,
		"x\n# ------------------------ >8 ------------------------\nEntire-Checkpoint: abc123def456\n": false,
		"x\n": false,
	}
	for msg, want := range cases {
		if got := HasCheckpointTrailerLine(msg); got != want {
			t.Errorf("HasCheckpointTrailerLine(%q) = %v, want %v", msg, got, want)
		}
	}
}
