package pve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func storageTestClient(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *Client {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(handler))
	t.Cleanup(ts.Close)

	c, err := New(Config{
		Host:               strings.TrimPrefix(ts.URL, "https://"),
		Node:               "node1",
		TokenID:            "user@pve!token=11111111-2222-3333-4444-555555555555",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestDownloadURLPostsExpectedFormAndPath(t *testing.T) {
	var gotMethod, gotPath string
	var gotForm map[string][]string
	c := storageTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"UPID:node1:00001234:1A2B3C4D:5E6F7A8B:download:100:user@pve!token:"}`))
	})

	upid, err := c.DownloadURL(context.Background(), "local", DownloadURLOptions{
		Content:           "import",
		Filename:          "debian-13.qcow2",
		URL:               "https://example.com/debian-13.qcow2",
		Checksum:          "deadbeef",
		ChecksumAlgorithm: "sha256",
	})
	if err != nil {
		t.Fatalf("DownloadURL: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q; want POST", gotMethod)
	}
	if gotPath != "/api2/json/nodes/node1/storage/local/download-url" {
		t.Errorf("path = %q", gotPath)
	}
	if got := gotForm["content"]; len(got) != 1 || got[0] != "import" {
		t.Errorf("content = %v", got)
	}
	if got := gotForm["checksum-algorithm"]; len(got) != 1 || got[0] != "sha256" {
		t.Errorf("checksum-algorithm = %v", got)
	}
	if upid.ID != "100" {
		t.Errorf("upid.ID = %q; want 100", upid.ID)
	}
}

func TestStorageContentListsVolumes(t *testing.T) {
	var gotMethod, gotPath string
	c := storageTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"volid":"local:import/debian-13.qcow2","content":"import","format":"qcow2","size":123456}
		]}`))
	})

	items, err := c.StorageContent(context.Background(), "local")
	if err != nil {
		t.Fatalf("StorageContent: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q; want GET", gotMethod)
	}
	if gotPath != "/api2/json/nodes/node1/storage/local/content" {
		t.Errorf("path = %q", gotPath)
	}
	if len(items) != 1 || items[0].VolID != "local:import/debian-13.qcow2" || items[0].Size != 123456 {
		t.Fatalf("items = %+v", items)
	}
}
