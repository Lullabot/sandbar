package provision

import (
	"testing"
	"testing/fstest"
)

func TestSetupVersionTracksFinalizeFilesOnly(t *testing.T) {
	files := fstest.MapFS{
		"site.yml":                       &fstest.MapFile{Data: []byte("site")},
		"roles/project/tasks/main.yml":   &fstest.MapFile{Data: []byte("project-v1")},
		"roles/dev-tools/tasks/main.yml": &fstest.MapFile{Data: []byte("base-v1")},
	}
	v1, err := SetupVersion(files)
	if err != nil {
		t.Fatal(err)
	}
	files["roles/dev-tools/tasks/main.yml"] = &fstest.MapFile{Data: []byte("base-v2")}
	baseOnly, err := SetupVersion(files)
	if err != nil {
		t.Fatal(err)
	}
	if baseOnly != v1 {
		t.Fatal("base-only edit changed setup revision")
	}
	files["roles/project/tasks/main.yml"] = &fstest.MapFile{Data: []byte("project-v2")}
	finalize, err := SetupVersion(files)
	if err != nil {
		t.Fatal(err)
	}
	if finalize == v1 {
		t.Fatal("finalize edit left setup revision current")
	}
}
