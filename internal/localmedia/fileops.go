package localmedia

// Shared file operations (rename / move / duplicate / delete) - one implementation for the
// local Library browse AND the remotectl endpoint, so a controller drives the controlled
// machine's filesystem with the same semantics it has locally.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ValidBaseName rejects empty names, path separators and dot-traversal.
func ValidBaseName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return errors.New("invalid name")
	}
	if strings.ContainsAny(name, `/\`) || filepath.Base(name) != name {
		return errors.New("name must not contain path separators")
	}
	return nil
}

// Rename renames path to newName (base name only) inside its directory; returns the new path.
func Rename(path, newName string) (string, error) {
	if err := ValidBaseName(newName); err != nil {
		return "", err
	}
	path = filepath.Clean(path)
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	dst := filepath.Join(filepath.Dir(path), strings.TrimSpace(newName))
	if dst == path {
		return dst, nil
	}
	// Same-name-different-case renames must pass on case-insensitive filesystems.
	if !strings.EqualFold(dst, path) {
		if _, err := os.Stat(dst); err == nil {
			return "", fmt.Errorf("%q already exists", newName)
		}
	}
	if err := os.Rename(path, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// Move moves path into destDir (same name); returns the new path. Files fall back to
// copy+delete across volumes; directories only move within one volume.
func Move(path, destDir string) (string, error) {
	path, destDir = filepath.Clean(path), filepath.Clean(destDir)
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	di, err := os.Stat(destDir)
	if err != nil {
		return "", err
	}
	if !di.IsDir() {
		return "", errors.New("destination is not a directory")
	}
	dst := filepath.Join(destDir, filepath.Base(path))
	if dst == path {
		return dst, nil
	}
	if fi.IsDir() && strings.HasPrefix(dst+string(filepath.Separator), path+string(filepath.Separator)) {
		return "", errors.New("cannot move a folder into itself")
	}
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("%q already exists in the destination", filepath.Base(path))
	}
	if err := os.Rename(path, dst); err != nil {
		if fi.IsDir() {
			return "", err // no cross-volume dir move (copy trees are too risky to do implicitly)
		}
		if cerr := copyFile(path, dst); cerr != nil {
			return "", cerr
		}
		if rerr := os.Remove(path); rerr != nil {
			return "", rerr
		}
	}
	return dst, nil
}

// Duplicate copies a file beside itself as "name copy[.ext]" (then "name copy 2", …);
// returns the copy's path. Directories are not duplicated.
func Duplicate(path string) (string, error) {
	path = filepath.Clean(path)
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", errors.New("folders cannot be duplicated")
	}
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	dst := stem + " copy" + ext
	for n := 2; ; n++ {
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			break
		}
		dst = fmt.Sprintf("%s copy %d%s", stem, n, ext)
	}
	if err := copyFile(path, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// Delete removes a file, or a directory recursively.
func Delete(path string) error {
	path = filepath.Clean(path)
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

// copyFile copies src → dst (fails if dst exists).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}
