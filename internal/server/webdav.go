package server

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/net/webdav"
)

type davFS struct{ root *os.Root }

// projectPath translates a project-relative protocol path, which uses slashes
// regardless of the operating system. Reject Windows drives and stream names.
func projectPath(name string) (string, error) {
	if strings.ContainsAny(name, "\\:\x00\r\n") {
		return "", fs.ErrInvalid
	}
	return filepath.Localize(name)
}

func davPath(name string) (string, error) {
	name = strings.Trim(name, "/")
	if name == "" {
		name = "."
	}
	return projectPath(name)
}

func (d davFS) Mkdir(_ context.Context, name string, mode os.FileMode) error {
	name, err := davPath(name)
	if err != nil {
		return err
	}
	return d.root.Mkdir(name, mode)
}

func (d davFS) OpenFile(_ context.Context, name string, flag int, mode os.FileMode) (webdav.File, error) {
	name, err := davPath(name)
	if err != nil {
		return nil, err
	}
	file, err := d.root.OpenFile(name, flag, mode)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (d davFS) RemoveAll(_ context.Context, name string) error {
	name, err := davPath(name)
	if err != nil {
		return err
	}
	if name == "." {
		return fs.ErrPermission
	}
	return d.root.RemoveAll(name)
}

func (d davFS) Rename(_ context.Context, oldName, newName string) error {
	oldName, err := davPath(oldName)
	if err != nil {
		return err
	}
	newName, err = davPath(newName)
	if err != nil {
		return err
	}
	if oldName == "." || newName == "." {
		return fs.ErrPermission
	}
	return d.root.Rename(oldName, newName)
}

func (d davFS) Stat(_ context.Context, name string) (os.FileInfo, error) {
	name, err := davPath(name)
	if err != nil {
		return nil, err
	}
	return d.root.Stat(name)
}
