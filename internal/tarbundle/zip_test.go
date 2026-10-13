package tarbundle

import "testing"

// A zip of files reads back as the same files; a path that is not a
// bundle path is refused.
func TestZipFilesRoundTrip(t *testing.T) {
	files := map[string][]byte{"index.html": []byte("<p>hi</p>"), "assets/app.js": []byte("1")}
	raw, err := ZipFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ReadArchive(raw, SiteLimits)
	if err != nil || len(back) != 2 || string(back["assets/app.js"]) != "1" {
		t.Fatalf("read back = %v %v", back, err)
	}
	if _, err := ZipFiles(map[string][]byte{"../x": nil}); err == nil {
		t.Fatal("zipped a path outside the bundle")
	}
}
