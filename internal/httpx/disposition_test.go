package httpx

import "testing"

func TestContentDisposition(t *testing.T) {
	for _, tc := range []struct{ disp, name, want string }{
		{"attachment", "notes.md", "attachment; filename=notes.md"},
		{"inline", "", "inline"},
		{"attachment", "报告.pdf", "attachment; filename*=utf-8''%E6%8A%A5%E5%91%8A.pdf"},
	} {
		if got := ContentDisposition(tc.disp, tc.name); got != tc.want {
			t.Errorf("ContentDisposition(%q, %q) = %q, want %q", tc.disp, tc.name, got, tc.want)
		}
	}
}
