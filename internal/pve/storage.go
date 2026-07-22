package pve

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// DownloadURLOptions configures POST .../storage/{storage}/download-url,
// which downloads a file directly on the PVE node into storage — used e.g.
// to fetch a cloud image once per node rather than uploading it through this
// client.
type DownloadURLOptions struct {
	// Content is the target content type, e.g. "iso", "vztmpl", "import".
	Content  string
	Filename string
	URL      string
	// Checksum and ChecksumAlgorithm are optional; when Checksum is set,
	// ChecksumAlgorithm must be too (e.g. "sha256").
	Checksum          string
	ChecksumAlgorithm string
}

func (o DownloadURLOptions) formValues() url.Values {
	form := url.Values{
		"content":  {o.Content},
		"filename": {o.Filename},
		"url":      {o.URL},
	}
	if o.Checksum != "" {
		form.Set("checksum", o.Checksum)
		form.Set("checksum-algorithm", o.ChecksumAlgorithm)
	}
	return form
}

// DownloadURL downloads a file into storage on this client's node via POST
// .../storage/{storage}/download-url, returning a UPID.
func (c *Client) DownloadURL(ctx context.Context, storage string, opts DownloadURLOptions) (UPID, error) {
	var raw string
	path := fmt.Sprintf("/nodes/%s/storage/%s/download-url", c.node, storage)
	if err := c.do(ctx, http.MethodPost, path, nil, opts.formValues(), &raw); err != nil {
		return UPID{}, err
	}
	return ParseUPID(raw)
}

// StorageContentItem is one entry of GET .../storage/{storage}/content.
type StorageContentItem struct {
	VolID   string `json:"volid"`
	Content string `json:"content"`
	Format  string `json:"format"`
	Size    int64  `json:"size"`
	Used    int64  `json:"used,omitempty"`
	// VMID is the owning VM/CT id, when the volume is disk/backup content
	// owned by one.
	VMID int `json:"vmid,omitempty"`
}

// StorageContent lists the volumes on storage via GET
// .../storage/{storage}/content.
func (c *Client) StorageContent(ctx context.Context, storage string) ([]StorageContentItem, error) {
	var items []StorageContentItem
	path := fmt.Sprintf("/nodes/%s/storage/%s/content", c.node, storage)
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &items); err != nil {
		return nil, err
	}
	return items, nil
}
