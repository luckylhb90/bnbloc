package store

import "testing"

func TestArtifactDownloadName(t *testing.T) {
	for _, tc := range []struct{ kind, file, title, want string }{
		{ArtifactKindFile, "report.pdf", "Report", "report.pdf"},
		{ArtifactKindMarkdown, "", "Notes", "Notes"},
		{ArtifactKindSite, "dist", "E2E App", "dist.tar"},
		{ArtifactKindSite, "site.TAR", "", "site.TAR"},
		{ArtifactKindSite, "", "", ""},
	} {
		if got := ArtifactDownloadName(tc.kind, tc.file, tc.title); got != tc.want {
			t.Errorf("ArtifactDownloadName(%q, %q, %q) = %q, want %q", tc.kind, tc.file, tc.title, got, tc.want)
		}
	}
}
