package wrapperinstall

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsureDownloadsVerifiesAndCaches(t *testing.T) {
	archiveName := "go-agy-acp-wrapper_1.2.3" + mustSuffix(t)
	archive := testArchive(t)
	digest := sha256.Sum256(archive)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/latest":
			_ = json.NewEncoder(w).Encode(release{TagName: "v1.2.3", Assets: []asset{
				{Name: archiveName, URL: serverURL(r) + "/archive"},
				{Name: "checksums.txt", URL: serverURL(r) + "/checksums"},
			}})
		case "/checksums":
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(digest[:]), archiveName)
		case "/archive":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cache := t.TempDir()
	first, err := Ensure(context.Background(), Options{CacheDir: cache, URL: server.URL + "/latest", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	firstRequests := requests
	second, err := Ensure(context.Background(), Options{CacheDir: cache, URL: server.URL + "/latest", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if first != second || requests != firstRequests+1 {
		t.Fatalf("cache not reused: first=%q second=%q requests=%d", first, second, requests)
	}
	if content, _ := os.ReadFile(first); string(content) != "wrapper" {
		t.Fatalf("installed content = %q", content)
	}
}

func TestEnsureRejectsChecksumMismatch(t *testing.T) {
	archiveName := "go-agy-acp-wrapper_1.2.3" + mustSuffix(t)
	archive := testArchive(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			_ = json.NewEncoder(w).Encode(release{TagName: "v1.2.3", Assets: []asset{
				{Name: archiveName, URL: serverURL(r) + "/archive"},
				{Name: "checksums.txt", URL: serverURL(r) + "/checksums"},
			}})
		case "/checksums":
			fmt.Fprintf(w, "%064d  %s\n", 0, archiveName)
		case "/archive":
			_, _ = w.Write(archive)
		}
	}))
	defer server.Close()
	cache := t.TempDir()
	if _, err := Ensure(context.Background(), Options{CacheDir: cache, URL: server.URL + "/latest", Client: server.Client()}); err == nil {
		t.Fatal("expected checksum error")
	}
	matches, _ := filepath.Glob(filepath.Join(cache, "v*", "go-agy-acp-wrapper*"))
	if len(matches) != 0 {
		t.Fatalf("bad archive was installed: %v", matches)
	}
}

func TestEnsureUsesCacheOffline(t *testing.T) {
	cache := t.TempDir()
	name := "go-agy-acp-wrapper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	cached := filepath.Join(cache, "v1.0.0", name)
	if err := os.MkdirAll(filepath.Dir(cached), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached, []byte("cached"), 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := Ensure(context.Background(), Options{CacheDir: cache, URL: "://bad"})
	if err != nil || path != cached {
		t.Fatalf("path=%q err=%v", path, err)
	}
}

func mustSuffix(t *testing.T) string {
	t.Helper()
	suffix, err := assetSuffix()
	if err != nil {
		t.Fatal(err)
	}
	return suffix
}
func testArchive(t *testing.T) []byte {
	t.Helper()
	name := "go-agy-acp-wrapper"
	if runtime.GOOS == "windows" {
		name += ".exe"
		var output bytes.Buffer
		writer := zip.NewWriter(&output)
		file, _ := writer.Create(name)
		_, _ = file.Write([]byte("wrapper"))
		_ = writer.Close()
		return output.Bytes()
	}
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: 7, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("wrapper"))
	_ = tw.Close()
	_ = gz.Close()
	return output.Bytes()
}
func serverURL(r *http.Request) string { return "http://" + r.Host }
