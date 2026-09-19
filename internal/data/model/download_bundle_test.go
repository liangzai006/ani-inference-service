package model

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundleDownloaderValidatesChecksumAndSafeCompleteArchive(t *testing.T) {
	for _, kind := range []string{"valid", "checksum", "traversal", "symlink", "incomplete"} {
		t.Run(kind, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			files := []string{"config.json", "tokenizer.json", "model.safetensors"}
			if kind == "traversal" {
				files = append(files, "../escaped")
			}
			if kind == "incomplete" {
				files = files[:2]
			}
			for _, name := range files {
				body := []byte("fixture")
				_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0444, Size: int64(len(body))})
				_, _ = tw.Write(body)
			}
			if kind == "symlink" {
				_ = tw.WriteHeader(&tar.Header{Name: "linked", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
			}
			_ = tw.Close()
			dir := t.TempDir()
			root := filepath.Join(dir, "volume")
			_ = os.Mkdir(root, 0700)
			archive, script := filepath.Join(dir, "archive.tar"), filepath.Join(dir, "download.py")
			if err := os.WriteFile(archive, buf.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(script, []byte(downloadBundle), 0600); err != nil {
				t.Fatal(err)
			}
			digest := fmt.Sprintf("%x", sha256.Sum256(buf.Bytes()))
			if kind == "checksum" {
				digest = strings.Repeat("0", 64)
			}
			// Replace only the HTTPS transport so the real downloader/extractor runs
			// without network or socket permissions.
			cmd := exec.Command("python3", "-c", "import os,runpy,urllib.request; urllib.request.urlopen=lambda *a,**k:open(os.environ['TEST_ARCHIVE'],'rb'); runpy.run_path(os.environ['TEST_SCRIPT'],run_name='__main__')")
			cmd.Env = append(os.Environ(), "MODEL_ROOT="+root, "MODEL_SHA256="+digest, "MODEL_DOWNLOAD_URL=https://storage/secret?token=never-log", "TEST_ARCHIVE="+archive, "TEST_SCRIPT="+script)
			output, err := cmd.CombinedOutput()
			if strings.Contains(string(output), "never-log") {
				t.Fatal("credential leaked")
			}
			if kind == "valid" {
				if err != nil {
					t.Fatalf("valid archive: %s %v", output, err)
				}
				if _, err = os.Stat(filepath.Join(root, "data", "model.safetensors")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatal("invalid bundle accepted")
				}
				if _, err = os.Stat(filepath.Join(root, "data")); !os.IsNotExist(err) {
					t.Fatal("invalid bundle was exposed to runtime")
				}
			}
		})
	}
}
