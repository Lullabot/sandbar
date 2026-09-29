package baseimage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
)

// Acquire returns an image path on the Lima host. The cache lives at
// <LIMA_HOME>/_sand/images/<version>/<filename> on that host. Existing files
// are hashed before use; new bytes are streamed to a unique temp file and only
// renamed into place after size and digest verification.
func Acquire(ctx context.Context, host lima.ImageStore, manifest Manifest, arch string, client *http.Client, out io.Writer) (string, error) {
	entry, err := manifest.ForArch(arch)
	if err != nil {
		return "", err
	}
	if err := entry.valid(); err != nil {
		return "", err
	}
	if manifest.Version == "" || strings.ContainsAny(manifest.Version, "/\\") || manifest.Version == "." || manifest.Version == ".." {
		return "", fmt.Errorf("invalid base-image version %q", manifest.Version)
	}
	if host.LimaHome() == "" {
		return "", errors.New("Lima home is unavailable for base-image cache")
	}
	if out == nil {
		out = io.Discard
	}
	if client == nil {
		client = http.DefaultClient
	}
	cachePath := path.Join(host.LimaHome(), "_sand", "images", manifest.Version, entry.Filename)
	if _, err := host.Stat(cachePath); err == nil {
		got, err := host.SHA256(ctx, cachePath)
		if err != nil {
			return "", fmt.Errorf("verifying cached base image: %w", err)
		}
		if got == entry.SHA256 {
			fmt.Fprintf(out, "==> base image cache hit: %s\n", cachePath)
			return cachePath, nil
		}
		fmt.Fprintf(out, "==> cached base image failed verification; downloading again\n")
		if err := host.RemoveAll(cachePath); err != nil {
			return "", fmt.Errorf("removing corrupt cached base image: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("checking base-image cache: %w", err)
	}

	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	tempPath := cachePath + ".tmp-" + hex.EncodeToString(nonce[:])
	defer host.RemoveAll(tempPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, entry.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading base image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading base image: HTTP %s", resp.Status)
	}

	fmt.Fprintf(out, "==> downloading base image: %s\n", entry.Filename)
	hash := sha256.New()
	progress := &downloadProgress{out: out, total: entry.Size, last: time.Now()}
	src := io.TeeReader(io.LimitReader(resp.Body, entry.Size+1), hash)
	if err := host.WriteStream(ctx, tempPath, io.TeeReader(src, progress)); err != nil {
		return "", fmt.Errorf("writing base image on Lima host: %w", err)
	}
	progress.finish()
	if progress.done != entry.Size {
		return "", fmt.Errorf("base-image size mismatch: expected %d bytes, got %d", entry.Size, progress.done)
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if got != entry.SHA256 {
		return "", fmt.Errorf("base-image digest mismatch: expected %s, got %s", entry.SHA256, got)
	}
	// Hash the host-side temp file too: the source stream's digest alone cannot
	// prove that the bytes survived the remote write or the local filesystem.
	onHost, err := host.SHA256(ctx, tempPath)
	if err != nil {
		return "", fmt.Errorf("verifying downloaded base image on Lima host: %w", err)
	}
	if onHost != entry.SHA256 {
		return "", fmt.Errorf("base-image digest mismatch on Lima host: expected %s, got %s", entry.SHA256, onHost)
	}
	if err := host.Rename(ctx, tempPath, cachePath); err != nil {
		return "", fmt.Errorf("publishing verified base image: %w", err)
	}
	fmt.Fprintf(out, "==> base image verified: %s\n", cachePath)
	return cachePath, nil
}

type downloadProgress struct {
	out                io.Writer
	total, done, shown int64
	last               time.Time
}

func (p *downloadProgress) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if p.done-p.shown >= 64<<20 || time.Since(p.last) >= 3*time.Second {
		p.show()
	}
	return len(b), nil
}

func (p *downloadProgress) show() {
	fmt.Fprintf(p.out, "==> downloading base image: %d / %d MiB\n", p.done>>20, p.total>>20)
	p.shown, p.last = p.done, time.Now()
}

func (p *downloadProgress) finish() {
	if p.done != p.shown {
		p.show()
	}
}
