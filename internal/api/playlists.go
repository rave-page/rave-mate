package api

// Playlist CRUD (GET/POST/PATCH/DELETE /playlists, PUT /playlists/{id}/items): hand-written
// over the redacted-logging doer (endpoint newer than the generated spec). Wire shapes mirror
// the web repo's generated models (PlaylistOut/PlaylistItemIn/...) exactly. PlaylistItemIn
// intentionally has NO path field - local file paths never go on the wire.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// maxPlaylistItems is the backend's PUT items cap.
const maxPlaylistItems = 1000

// PlaylistOut is one playlist on the wire (FE PlaylistOut).
type PlaylistOut struct {
	ID          string            `json:"id"` // pl_…
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Visibility  string            `json:"visibility"` // public|unlisted|private
	OwnerUserID string            `json:"owner_user_id"`
	Access      string            `json:"access"`      // owner|shared|public
	SharedRole  string            `json:"shared_role"` // viewer|editor, "" when not grant-derived
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
	Items       []PlaylistItemOut `json:"items,omitempty"` // only with ?include=items or PUT response
}

// PlaylistItemOut is one playlist item on the wire (FE PlaylistItemOut).
type PlaylistItemOut struct {
	ID               string `json:"id"` // pli_…
	Position         int    `json:"position"`
	Title            string `json:"title"`
	ArtistText       string `json:"artist_text"`
	CanonicalTrackID string `json:"canonical_track_id"` // trk_…, "" when unlinked
	LibraryTrackID   string `json:"library_track_id"`   // lib_…, "" when unlinked
	ArtworkURL       string `json:"artwork_url"`
}

// PlaylistItemIn is one item for the replace-all PUT (FE PlaylistItemIn - no position field,
// order = array order; no path field by design).
type PlaylistItemIn struct {
	Title            string `json:"title,omitempty"`
	ArtistText       string `json:"artist_text,omitempty"`
	CanonicalTrackID string `json:"canonical_track_id,omitempty"`
	LibraryTrackID   string `json:"library_track_id,omitempty"` // must be the caller's own lib row
}

// playlistReq issues one authed JSON request and decodes the response into out (nil = discard).
func (c *Client) playlistReq(ctx context.Context, token, method, path string, body, out any) error {
	if token == "" {
		return fmt.Errorf("playlists: unauthenticated")
	}
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.bulkDoer.Do(req)
	if err != nil {
		return err
	}
	return decode(resp, out)
}

// ListPlaylists returns the caller's playlists (owner + shared).
func (c *Client) ListPlaylists(ctx context.Context, token string) ([]PlaylistOut, error) {
	var out struct {
		Playlists []PlaylistOut `json:"playlists"`
	}
	err := c.playlistReq(ctx, token, http.MethodGet, "/playlists", nil, &out)
	return out.Playlists, err
}

// GetPlaylist fetches one playlist, optionally with its items.
func (c *Client) GetPlaylist(ctx context.Context, token, id string, includeItems bool) (PlaylistOut, error) {
	p := "/playlists/" + url.PathEscape(id)
	if includeItems {
		p += "?include=items"
	}
	var out PlaylistOut
	err := c.playlistReq(ctx, token, http.MethodGet, p, nil, &out)
	return out, err
}

// CreatePlaylist creates a playlist (visibility "" = backend default private).
func (c *Client) CreatePlaylist(ctx context.Context, token, title, description, visibility string) (PlaylistOut, error) {
	body := map[string]string{"title": title}
	if description != "" {
		body["description"] = description
	}
	if visibility != "" {
		body["visibility"] = visibility
	}
	var out PlaylistOut
	err := c.playlistReq(ctx, token, http.MethodPost, "/playlists", body, &out)
	return out, err
}

// UpdatePlaylist patches title/description/visibility ("" = leave unchanged).
func (c *Client) UpdatePlaylist(ctx context.Context, token, id, title, description, visibility string) (PlaylistOut, error) {
	body := map[string]string{}
	if title != "" {
		body["title"] = title
	}
	if description != "" {
		body["description"] = description
	}
	if visibility != "" {
		body["visibility"] = visibility
	}
	var out PlaylistOut
	err := c.playlistReq(ctx, token, http.MethodPatch, "/playlists/"+url.PathEscape(id), body, &out)
	return out, err
}

// DeletePlaylist removes a playlist.
func (c *Client) DeletePlaylist(ctx context.Context, token, id string) error {
	return c.playlistReq(ctx, token, http.MethodDelete, "/playlists/"+url.PathEscape(id), nil, nil)
}

// PutPlaylistItems replaces the playlist's full ordered item set (≤1000) and returns the
// playlist with its post-replace items (server may link canonical ids from library rows).
func (c *Client) PutPlaylistItems(ctx context.Context, token, id string, items []PlaylistItemIn) (PlaylistOut, error) {
	if len(items) > maxPlaylistItems {
		return PlaylistOut{}, fmt.Errorf("playlist items: %d exceeds %d", len(items), maxPlaylistItems)
	}
	body := map[string]any{"items": items}
	var out PlaylistOut
	err := c.playlistReq(ctx, token, http.MethodPut, "/playlists/"+url.PathEscape(id)+"/items", body, &out)
	return out, err
}
