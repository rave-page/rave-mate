package ui

// mediaOps is the Library's file backend: one contract for local and remote (paired
// instance) rows, so browse + context-menu file ops behave identically against either.
// All calls may block - invoke off the UI thread.

import (
	"context"

	"rave.page/mate/internal/localmedia"
	"rave.page/mate/internal/remotectl"
)

type mediaOps interface {
	Remote() bool // true = operates on a paired instance's filesystem
	ListDir(ctx context.Context, path string) (localmedia.Listing, error)
	Defaults(ctx context.Context) (localmedia.DefaultPaths, error)
	Rename(ctx context.Context, path, newName string) (string, error)
	Move(ctx context.Context, path, destDir string) (string, error)
	Duplicate(ctx context.Context, path string) (string, error)
	Delete(ctx context.Context, path string) error
}

// localOps runs against this computer's filesystem.
type localOps struct{}

func (localOps) Remote() bool { return false }
func (localOps) ListDir(_ context.Context, path string) (localmedia.Listing, error) {
	return localmedia.ListDirectory(path, false), nil
}
func (localOps) Defaults(context.Context) (localmedia.DefaultPaths, error) {
	return localmedia.Defaults(), nil
}
func (localOps) Rename(_ context.Context, path, newName string) (string, error) {
	return localmedia.Rename(path, newName)
}
func (localOps) Move(_ context.Context, path, destDir string) (string, error) {
	return localmedia.Move(path, destDir)
}
func (localOps) Duplicate(_ context.Context, path string) (string, error) {
	return localmedia.Duplicate(path)
}
func (localOps) Delete(_ context.Context, path string) error { return localmedia.Delete(path) }

// remoteOps runs against a paired instance via remotectl.
type remoteOps struct{ client *remotectl.Client }

func (remoteOps) Remote() bool { return true }
func (r remoteOps) ListDir(ctx context.Context, path string) (localmedia.Listing, error) {
	return r.client.ListDirectory(ctx, path, false)
}
func (r remoteOps) Defaults(ctx context.Context) (localmedia.DefaultPaths, error) {
	return r.client.GetDefaults(ctx)
}
func (r remoteOps) Rename(ctx context.Context, path, newName string) (string, error) {
	return r.client.RenamePath(ctx, path, newName)
}
func (r remoteOps) Move(ctx context.Context, path, destDir string) (string, error) {
	return r.client.MovePath(ctx, path, destDir)
}
func (r remoteOps) Duplicate(ctx context.Context, path string) (string, error) {
	return r.client.DuplicatePath(ctx, path)
}
func (r remoteOps) Delete(ctx context.Context, path string) error {
	return r.client.DeletePath(ctx, path)
}
