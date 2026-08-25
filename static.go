package zip

import (
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
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

// Static returns a leaf Handler that serves files from fsys. Register it on a
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
//     honours HEAD and If-Modified-Since (304).
//   - Advertises Accept-Ranges and serves one byte range per request: 206 with
//     Content-Range, 416 for a span the file does not have, and the whole file
//     for a Range it declines to honour. If-Range is respected. A file whose
//     fs.File cannot seek is always served whole.
//   - Nothing else — no compression, no directory listing.
//
// fsys is any fs.FS: an embed.FS for baked-in assets or os.DirFS(dir) for a
// directory on disk. Both are traversal-safe by construction; the fs.ValidPath
// gate is defence in depth on top of that.
func Static(fsys fs.FS, opts ...StaticOption) Handler {
	var cfg staticConfig
	for _, o := range opts {
		o(&cfg)
	}
	// TERMINAL: it answers the address it is registered at, so composing it
	// with Use — where it would answer every address beneath — is refused at
	// build time rather than serving files in place of the program.
	return Terminal("zip.Static", func(c *Ctx) error {
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
	})
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

// serveFile names the content type Static is willing to guess — the stored
// file's own extension — and hands the rest to [Content], which owns the
// conditional answer, the range and the ownership of f.
func serveFile(c *Ctx, f fs.File, info fs.FileInfo) error {
	ctype := mime.TypeByExtension(path.Ext(info.Name()))
	if ctype == "" {
		ctype = fiber.MIMEOctetStream
	}
	c.fc.Set(fiber.HeaderContentType, ctype)
	return Content(c, info.ModTime(), info.Size(), f)
}

// Content writes an already-open representation to the response: Last-Modified
// with a conditional 304, Accept-Ranges, and either one byte range (206 with
// Content-Range, or 416 for a span the representation does not have) or the
// whole thing. It is what [Static] serves files with, exported for a handler
// that needs the rest of the answer to be its own.
//
// The CALLER owns Content-Type and any Content-Encoding. Only the caller knows
// whether these bytes are the file it named or a precompressed sibling of it —
// a .br beside main.css is still text/css on the wire, and guessing from the
// stored name would label it as brotli.
//
// Ranges need seeking, so a src that is not an [io.Seeker] is served whole,
// which RFC 9110 §14.2 permits for any Range. Content takes ownership: src is
// closed if it is an [io.Closer], on every path including 304 and 416.
func Content(c *Ctx, mod time.Time, size int64, src io.Reader) error {
	closeSrc := func() {
		if rc, ok := src.(io.Closer); ok {
			_ = rc.Close()
		}
	}
	if !mod.IsZero() {
		c.fc.Set(fiber.HeaderLastModified, mod.UTC().Format(http.TimeFormat))
		if ims := c.fc.Get(fiber.HeaderIfModifiedSince); ims != "" {
			if t, err := http.ParseTime(ims); err == nil && !mod.Truncate(time.Second).After(t) {
				closeSrc() // not streaming — release it ourselves
				c.fc.Status(fiber.StatusNotModified)
				return nil
			}
		}
	}
	c.fc.Set(fiber.HeaderAcceptRanges, "bytes")

	start, length, status := int64(0), size, fiber.StatusOK
	// Seeking is what makes a range cheap; a source that cannot seek is served
	// whole, which RFC 9110 §14.2 allows for any Range at all.
	seeker, seekable := src.(io.Seeker)
	if seekable && rangeApplies(c, mod) {
		start, length, status = parseRange(c.fc.Get(fiber.HeaderRange), size)
	}

	switch status {
	case fiber.StatusRequestedRangeNotSatisfiable:
		closeSrc()
		c.fc.Set(fiber.HeaderContentRange, "bytes */"+strconv.FormatInt(size, 10))
		c.fc.Status(status)
		return nil
	case fiber.StatusPartialContent:
		if _, err := seeker.Seek(start, io.SeekStart); err != nil {
			closeSrc()
			return err
		}
		c.fc.Set(fiber.HeaderContentRange,
			"bytes "+strconv.FormatInt(start, 10)+"-"+
				strconv.FormatInt(start+length-1, 10)+"/"+
				strconv.FormatInt(size, 10))
		c.fc.Status(status)
		return c.fc.SendStream(extent{Reader: io.LimitReader(src, length), src: src}, int(length))
	}
	c.fc.Status(fiber.StatusOK)
	return c.fc.SendStream(src, int(size)) // fasthttp closes src after writing
}

// extent is the contiguous byte range of an open file that a 206 writes.
// io.LimitReader alone is not an io.Closer, so fasthttp would write the body
// and leave the file open; carrying the file lets the one SendStream call own
// it exactly as it owns the whole-file case.
type extent struct {
	io.Reader
	src io.Reader
}

func (e extent) Close() error {
	if rc, ok := e.src.(io.Closer); ok {
		return rc.Close()
	}
	return nil
}

// rangeApplies reports whether an If-Range precondition still holds. Absent, it
// holds trivially. Static publishes no entity tag, so only the date form can
// match, and RFC 9110 §13.1.5 wants that match exact: a representation that has
// changed at all is served whole rather than spliced from two versions.
func rangeApplies(c *Ctx, mod time.Time) bool {
	v := strings.TrimSpace(c.fc.Get(fiber.HeaderIfRange))
	if v == "" {
		return true
	}
	if mod.IsZero() {
		return false
	}
	t, err := http.ParseTime(v)
	return err == nil && mod.Truncate(time.Second).Equal(t.Truncate(time.Second))
}

// parseRange resolves an RFC 9110 §14.1 Range value against size, answering
// with the status the request has earned:
//
//	StatusOK           nothing to honour — no header, a unit we do not speak,
//	                   more than one range, or a span we decline. A server may
//	                   always answer in full (§14.2), so this is never an error.
//	StatusPartialContent  one satisfiable span, returned as start and length.
//	StatusRequestedRangeNotSatisfiable  a span wholly outside the file.
//
// The distinction that matters is the last one: declining a range and refusing
// it are different answers, and a client that asked for byte 5,000 of a
// 100-byte file needs to be told, not handed the file.
func parseRange(v string, size int64) (start, length int64, status int) {
	const unit = "bytes="
	v = strings.TrimSpace(v)
	if v == "" || !strings.HasPrefix(v, unit) {
		return 0, size, fiber.StatusOK
	}
	spec := strings.TrimSpace(strings.TrimPrefix(v, unit))
	// A multi-range request is answered whole rather than as multipart/byteranges.
	if spec == "" || strings.Contains(spec, ",") {
		return 0, size, fiber.StatusOK
	}
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return 0, size, fiber.StatusOK
	}
	// An empty file has no byte to name, so every span over it is unsatisfiable.
	if size == 0 {
		return 0, 0, fiber.StatusRequestedRangeNotSatisfiable
	}
	first, last := strings.TrimSpace(spec[:dash]), strings.TrimSpace(spec[dash+1:])
	if first == "" { // suffix form: the final n bytes
		n, err := strconv.ParseInt(last, 10, 64)
		if err != nil || n <= 0 {
			return 0, size, fiber.StatusRequestedRangeNotSatisfiable
		}
		if n > size {
			n = size
		}
		return size - n, n, fiber.StatusPartialContent
	}
	s, err := strconv.ParseInt(first, 10, 64)
	if err != nil || s < 0 {
		return 0, size, fiber.StatusOK
	}
	if s >= size {
		return 0, size, fiber.StatusRequestedRangeNotSatisfiable
	}
	e := size - 1
	if last != "" {
		if e, err = strconv.ParseInt(last, 10, 64); err != nil || e < s {
			return 0, size, fiber.StatusOK
		}
		if e >= size {
			e = size - 1
		}
	}
	return s, e - s + 1, fiber.StatusPartialContent
}
