package zip

import (
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/zap-proto/fiber/v3"
)

// StaticOption configures Static. Zero options serve fsys as-is.
type StaticOption func(*staticConfig)

type staticConfig struct {
	fallback    string
	index       string // dir-request index document (e.g. "index.html"); "" disables.
	stripPrefix string // request-path prefix to strip; "" uses the route's "*" capture.
}

// WithFallback serves name (e.g. "index.html") when the requested file does
// not exist — the SPA deep-link idiom: client-side routes resolve to the app
// shell instead of 404/next-route. Traversal-invalid paths still fail closed.
func WithFallback(name string) StaticOption {
	return func(c *staticConfig) { c.fallback = name }
}

// WithIndex serves name (e.g. "index.html") for directory and root requests.
// Without it a directory request falls through via c.Next().
func WithIndex(name string) StaticOption {
	return func(c *staticConfig) { c.index = name }
}

// WithStripPrefix derives the fs path from the request path with prefix
// removed, instead of from the route's "*" capture. Use it when the captured
// subpath is not the fs path — e.g. a versioned URL served from an unversioned
// tree: WithStripPrefix("/static/v2/") maps /static/v2/app.js to app.js.
func WithStripPrefix(prefix string) StaticOption {
	return func(c *staticConfig) { c.stripPrefix = prefix }
}

// Static returns a leaf Handler that serves files from fsys. Mount it on a
// wildcard route; the "*" capture selects the file:
//
//	app.Get("/assets/*", zip.Static(assets))                       // embed.FS
//	app.Get("/app/*", zip.Static(os.DirFS("dist"), zip.WithIndex("index.html")))
//
// Contract:
//   - The subpath is cleaned and checked with fs.ValidPath; any ".." escape or
//     absolute path is rejected fail-closed with 404 — Static can never read
//     outside fsys.
//   - A missing file yields c.Next(), so a later more-specific route or a SPA
//     catch-all still wins — never a 500.
//   - Sets Content-Type (by extension), Content-Length and Last-Modified;
//     honours HEAD and If-Modified-Since (304). Nothing else — no compression,
//     no byte ranges, no directory listing.
//
// fsys is any fs.FS: an embed.FS for baked-in assets or os.DirFS(dir) for a
// directory on disk. Both are traversal-safe by construction; the fs.ValidPath
// gate is defence in depth on top of that.
func Static(fsys fs.FS, opts ...StaticOption) Handler {
	var cfg staticConfig
	for _, o := range opts {
		o(&cfg)
	}
	return func(c *Ctx) error {
		sub := c.fc.Params("*")
		if cfg.stripPrefix != "" {
			sub = strings.TrimPrefix(c.fc.Path(), cfg.stripPrefix)
		}
		name, ok := staticClean(sub, cfg.index)
		if !ok {
			return ErrNotFound("not found") // traversal / absolute → fail closed
		}
		if name == "" {
			return c.Next() // dir/root with no index → let a later route win
		}
		f, info, ok := cfg.open(fsys, name)
		if !ok && cfg.fallback != "" && name != cfg.fallback {
			f, info, ok = cfg.open(fsys, cfg.fallback) // SPA fallback: serve the shell
		}
		if !ok {
			return c.Next() // missing → SPA catch-all / next route wins
		}
		return serveFile(c, f, info)
	}
}

// staticClean maps a raw wildcard subpath to a validated fs path. ok=false is
// the fail-closed traversal guard: any path that escapes the root (".."), is
// absolute, or is otherwise not an fs.ValidPath is rejected. path.Clean first
// collapses in-root navigation ("a/../b" → "b") so only genuine escapes fail.
// A bare root/dir request maps to index, or to "" (nothing to serve here) when
// no index is set.
func staticClean(sub, index string) (name string, ok bool) {
	sub = strings.TrimPrefix(sub, "/")
	if sub == "" {
		return index, true // "" when no index → handler yields via c.Next()
	}
	clean := path.Clean(sub)
	if !fs.ValidPath(clean) {
		return "", false
	}
	return clean, true
}

// open resolves name to a readable file, following a directory to its index
// document when configured. ok=false for missing files, directories without an
// index, and unreadable entries — all of which the caller turns into c.Next().
func (cfg staticConfig) open(fsys fs.FS, name string) (fs.File, fs.FileInfo, bool) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, nil, false
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, false
	}
	if !info.IsDir() {
		return f, info, true
	}
	// Directory: serve its index document, or fall through.
	_ = f.Close()
	if cfg.index == "" {
		return nil, nil, false
	}
	f, err = fsys.Open(path.Join(name, cfg.index))
	if err != nil {
		return nil, nil, false
	}
	info, err = f.Stat()
	if err != nil || info.IsDir() {
		_ = f.Close()
		return nil, nil, false
	}
	return f, info, true
}

// serveFile writes info's bytes to the response: Last-Modified + conditional
// 304, Content-Type by extension, then the body streamed with a known size so
// Content-Length is set. It takes ownership of f — fasthttp closes the stream
// after writing the body (or after skipping it for HEAD, which it detects via
// Response.SkipBody and still emits Content-Length for), so the one SendStream
// call serves GET and HEAD alike. The only path that does not stream — a 304 —
// closes f itself.
func serveFile(c *Ctx, f fs.File, info fs.FileInfo) error {
	if mod := info.ModTime(); !mod.IsZero() {
		c.fc.Set(fiber.HeaderLastModified, mod.UTC().Format(http.TimeFormat))
		if ims := c.fc.Get(fiber.HeaderIfModifiedSince); ims != "" {
			if t, err := http.ParseTime(ims); err == nil && !mod.Truncate(time.Second).After(t) {
				_ = f.Close() // not streaming — release it ourselves
				c.fc.Status(fiber.StatusNotModified)
				return nil
			}
		}
	}
	ctype := mime.TypeByExtension(path.Ext(info.Name()))
	if ctype == "" {
		ctype = fiber.MIMEOctetStream
	}
	c.fc.Set(fiber.HeaderContentType, ctype)
	c.fc.Status(fiber.StatusOK)
	return c.fc.SendStream(f, int(info.Size())) // fasthttp closes f after writing
}
