package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// uploadedArtifact is a file staged in the gateway's content-addressed
// artifact store (POST /v1/platform/artifacts).
type uploadedArtifact struct {
	Key    string
	SHA256 string
	Size   int64
	// URL is the gateway download URL a package / release pins.
	URL string
}

// fileDigest returns the sha256 (hex) and size of the file at path.
func fileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	return readerDigest(f)
}

func readerDigest(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// uploadArtifactFile is the ONE artifact upload of the CLI (sidecar binaries
// and desktop updater archives). It hashes the local file, streams it to the
// gateway (no total deadline), and refuses a response whose key / sha256 /
// size does not match the local file.
func uploadArtifactFile(c *client, path string) (uploadedArtifact, error) {
	localSHA, localSize, err := fileDigest(path)
	if err != nil {
		return uploadedArtifact{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return uploadedArtifact{}, err
	}
	defer f.Close()
	body, err := c.doReader(context.Background(), http.MethodPost, "/v1/platform/artifacts", f, "application/octet-stream")
	if err != nil {
		return uploadedArtifact{}, err
	}
	var out struct {
		Key    string `json:"key"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return uploadedArtifact{}, fmt.Errorf("artifact upload response invalid: %w", err)
	}
	if err := checkUploadResponse(out.Key, out.SHA256, out.Size, localSHA, localSize); err != nil {
		return uploadedArtifact{}, err
	}
	return uploadedArtifact{Key: out.Key, SHA256: out.SHA256, Size: out.Size, URL: c.cfg.Gateway + "/v1/artifacts/" + out.Key}, nil
}

// verifyArtifactDownload fetches a staged artifact back through the gateway
// (authenticated, as clients will) and checks its sha256 and size.
func verifyArtifactDownload(c *client, a uploadedArtifact) error {
	return c.stream(context.Background(), "/v1/artifacts/"+a.Key, func(r io.Reader) error {
		sha, size, err := readerDigest(r)
		if err != nil {
			return fmt.Errorf("download %s: %w", a.Key, err)
		}
		if !strings.EqualFold(sha, a.SHA256) || size != a.Size {
			return fmt.Errorf("downloaded artifact does not match the upload (sha256 %s, %d bytes; want %s, %d bytes)", sha, size, a.SHA256, a.Size)
		}
		return nil
	})
}
