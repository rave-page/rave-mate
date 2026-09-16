//go:build !windows && !linux

package shellplaces

// systemPlaces has no readable source on the remaining platforms. macOS: the Finder sidebar
// favourites live in the com.apple.sidebarlists / com.apple.finder bookmark blobs (opaque binary
// bookmark data), not readably parseable without the CoreServices Bookmark API - so it is left
// empty. The webui PLACES group (known user folders) still covers the common directories.
func systemPlaces() []Place { return nil }
