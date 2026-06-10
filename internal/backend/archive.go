package backend

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var defaultIgnoreDirs = map[string]bool{
	".git":         true,
	".palbase":     true,
	"node_modules": true,
}

// BuildTarball walks dir and returns a gzip-compressed tar with paths relative
// to dir (no wrapper directory), matching what /internal/push expects. It skips
// defaultIgnoreDirs and any glob in an optional .palignore file at the root.
func BuildTarball(dir string) ([]byte, error) {
	patterns, err := loadPalignore(filepath.Join(dir, ".palignore"))
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	walkErr := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		top := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		if info.IsDir() {
			if defaultIgnoreDirs[top] {
				return filepath.SkipDir
			}
			return nil
		}
		if defaultIgnoreDirs[top] || matchesAny(filepath.ToSlash(rel), patterns) {
			return nil
		}
		return writeTarFile(tw, dir, rel)
	})
	if walkErr != nil {
		return nil, walkErr
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeTarFile(tw *tar.Writer, dir, rel string) error {
	full := filepath.Join(dir, rel)
	fi, err := os.Stat(full)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return err
	}
	hdr.Name = filepath.ToSlash(rel)
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	f, err := os.Open(full)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

func loadPalignore(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var pats []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pats = append(pats, line)
	}
	return pats, sc.Err()
}

func matchesAny(rel string, patterns []string) bool {
	base := filepath.Base(rel)
	for _, p := range patterns {
		if ok, _ := filepath.Match(p, rel); ok {
			return true
		}
		if ok, _ := filepath.Match(p, base); ok {
			return true
		}
	}
	return false
}
