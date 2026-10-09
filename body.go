// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"reflect"
	"strings"

	"github.com/valyala/fasthttp"
	"github.com/zap-proto/fiber/v3"

	"github.com/zap-proto/zip/internal/jsonenc"
	"github.com/zap-proto/zip/internal/jsontag"
)

// Body is bytes and the media type that names them.
//
// As a field of an op's In it is the request body exactly as it arrived. The
// REST door puts the request's bytes and its Content-Type in it and decodes
// nothing; the input's other fields still bind from the path, the query, the
// declared headers and the declared cookies. An upload, a pack stream, a
// statement file and a signed webhook are typed ops this way, and the wire
// they speak does not move:
//
//	type ScanIn struct {
//	    Org  string   `json:"org" url:"org"`
//	    Body zip.Body `json:"body"` // the receipt, as sent
//	}
//	zip.Post(app, "/v1/books/:org/scan", scan, zip.Consumes("application/pdf", "image/png"))
//
// Over MCP, the CLI and the call plane the input arrives as one value, and the
// field is its bytes as a base64 string; its Type is then the first media type
// the op [Consumes].
//
// As an op's Out it is the answer: Bytes, or Reader read to its end and written
// chunk by chunk as it arrives, under Type, with Name as the filename a client
// saves it under (Content-Disposition: attachment). Status and Header are the
// status and headers it states; each must be one the op declared
// ([WithStatus], [WithResponseHeader]), as for any answer. An answer with no
// bytes and no Type carries no Content-Type, so a 304 is a Body whose Status is
// 304 and nothing else.
//
// The document publishes a request Body as the op's [Consumes] media with
// {type: string, format: binary}, and an answer as its [Produces] media the
// same way.
type Body struct {
	// Type is the media type: the request's Content-Type as sent, or the
	// answer's.
	Type string
	// Bytes are the bytes, exactly as received or as written.
	Bytes []byte
	// Name is the filename an answer is saved under. Empty sends no
	// Content-Disposition.
	Name string
	// Status is the answer's status, zero for the op's own. A request ignores it.
	Status int
	// Header are the answer's headers beside Content-Type and
	// Content-Disposition. A request ignores them.
	Header map[string]string `zap:"-"`
	// Reader, when set, is read to its end and written as it arrives in place
	// of Bytes. It is closed afterwards when it is an io.Closer.
	Reader io.Reader `zap:"-"`
}

// StatusCode is the status the answer states; zero is the op's own.
func (b Body) StatusCode() int { return b.Status }

// body is the Body an answer is, promoted to a type that embeds one: a
// [Verbatim], or a page that also sets a cookie.
func (b *Body) body() *Body { return b }

// answerBody is an answer that is a [Body], or embeds one.
type answerBody interface{ body() *Body }

// drain reads Reader into Bytes, for a transport whose answer is one message.
func (b *Body) drain() error {
	r := b.Reader
	b.Reader = nil
	if c, ok := r.(io.Closer); ok {
		defer func() { _ = c.Close() }()
	}
	got, err := io.ReadAll(r)
	b.Bytes = got
	return err
}

// release closes Reader, when it is an io.Closer, for an answer refused after
// its handler ran: a relay's Reader is an upstream body, and nothing else will
// read it. A nil Body releases nothing.
func (b *Body) release() {
	if b == nil {
		return
	}
	if c, ok := b.Reader.(io.Closer); ok {
		_ = c.Close()
	}
}

// ResponseHeaders are the headers the answer states.
func (b Body) ResponseHeaders() map[string]string { return b.Header }

// MarshalJSON writes the bytes as one base64 string, the form an argument
// object carries them in.
func (b Body) MarshalJSON() ([]byte, error) { return json.Marshal(b.Bytes) }

// UnmarshalJSON reads the bytes from one base64 string.
func (b *Body) UnmarshalJSON(data []byte) error {
	var raw []byte
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("a body is one base64 string: %w", err)
	}
	b.Bytes = raw
	return nil
}

// JSONSchema is the body as an argument object carries it.
func (Body) JSONSchema() map[string]any {
	return map[string]any{"type": "string", "contentEncoding": "base64"}
}

// File is one part of a multipart form: the filename the client gave it, its
// media type and its bytes. A field of type File, *File, []File or []*File in
// an op's In binds the part (or parts) of its form name.
//
// An argument object carries it as {"name", "type", "bytes"}, the bytes in
// base64, so a filename the handler reads survives MCP and the CLI.
type File struct {
	// Name is the filename the part was sent under.
	Name string `json:"name,omitempty"`
	// Type is the part's media type.
	Type string `json:"type,omitempty"`
	// Bytes are the part's bytes.
	Bytes []byte `json:"bytes"`
}

// Parser is an input that decodes its own request body. The REST door hands it
// the request's Content-Type and bytes instead of decoding JSON, so an op can
// read YAML or JSON, apply an RFC 7386 merge patch, refuse an unknown field,
// cap its own size or decode tolerantly, and still be a typed op: the document
// publishes the input's reflected schema under the media the op [Consumes]
// (application/json when it names none).
//
// An error that is an [HTTPError] is answered as it is, so a cap answers its
// own 413; any other error is 400 "invalid body". An argument object (MCP, the
// CLI) is handed over as application/json; the call plane carries the input as
// a ZAP message and does not parse.
type Parser interface {
	Parse(media string, body []byte) error
}

// Consumes names the media types an op's request body may arrive in. It is
// what the document publishes the request body under.
//
// Without it an op with a [Body] field consumes application/octet-stream, one
// that takes a [File] multipart/form-data, and any other application/json.
// form: fields bind a form only when the op consumes one: name
// application/x-www-form-urlencoded or multipart/form-data here. Without that
// the tags are another binder's and the op reads JSON as it always did.
func Consumes(media ...string) OpOption {
	if len(media) == 0 {
		panic("zip: Consumes needs at least one media type")
	}
	return func(op *registeredOp) { op.Consumes = append([]string(nil), media...) }
}

// The media types a request kind defaults to.
const (
	mimeOctet     = "application/octet-stream"
	mimeForm      = "application/x-www-form-urlencoded"
	mimeMultipart = "multipart/form-data"
)

var (
	bodyType   = reflect.TypeOf(Body{})
	fileType   = reflect.TypeOf(File{})
	parserType = reflect.TypeOf((*Parser)(nil)).Elem()
)

// intake is how an op's input arrives, read once from In at registration and
// settled against the media the op consumes.
type intake struct {
	// raw is the index path of In's [Body] field, nil when it has none.
	raw []int
	// files says In takes a multipart part ([File]); fields says it has form:
	// fields.
	files, fields bool
	// form says the op reads its body as a form: it takes a part, or it has
	// form: fields and consumes a form media type.
	form bool
	// parse says *In decodes its own body ([Parser]).
	parse bool
	// cookies says In declares a cookie: field.
	cookies bool
}

// intakeOf reads the request kind off In, refusing a shape the wire cannot
// carry rather than registering an op that misreads it.
func intakeOf(in reflect.Type) intake {
	var r intake
	t := in
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return r
	}
	r.parse = reflect.PointerTo(t).Implements(parserType)
	for _, f := range wireFields(t) {
		switch {
		case f.Type == bodyType:
			if r.raw != nil {
				panic(fmt.Sprintf("zip: %s takes two zip.Body fields; a request has one body", t))
			}
			r.raw = f.Index
		case isFile(f.Type):
			r.files = true
		case formFieldName(f) != "":
			r.fields = true
		case f.Type.Kind() == reflect.Pointer && f.Type.Elem() == bodyType:
			panic(fmt.Sprintf("zip: %s.%s is a *zip.Body; a request body is always there, so declare zip.Body", t, f.Name))
		}
		if cookieFieldName(f) != "" {
			r.cookies = true
		}
	}
	return r
}

// settle decides whether the body is a form, from the media the op consumes,
// and refuses a shape the wire cannot carry rather than registering an op that
// misreads it.
func (r *intake) settle(method string, in reflect.Type, media []string) {
	r.form = r.files || r.fields && formMedia(media)
	kinds := 0
	for _, k := range []bool{r.raw != nil, r.form, r.parse} {
		if k {
			kinds++
		}
	}
	if kinds > 1 {
		panic(fmt.Sprintf("zip: %s takes its body more than one way (a zip.Body field, a form, a Parse method); a request body is read once", in))
	}
	if kinds > 0 && !hasBody(method) {
		panic(fmt.Sprintf("zip: %s carries no request body, so %s cannot take one", method, in))
	}
}

// whole says the op takes the request body itself — as bytes, as a form or
// through a Parser — rather than as one JSON value the seam decodes.
func (r intake) whole() bool { return r.raw != nil || r.form || r.parse }

// formMedia reports whether a media list names a form encoding.
func formMedia(media []string) bool {
	for _, m := range media {
		if isFormMedia(m) {
			return true
		}
	}
	return false
}

// consumes is the request media an op names, defaulted by its request kind.
// Nil is application/json.
func consumes(op *registeredOp) []string {
	if len(op.Consumes) > 0 {
		return op.Consumes
	}
	switch {
	case op.req.raw != nil:
		return []string{mimeOctet}
	case op.req.files:
		return []string{mimeMultipart}
	}
	return nil
}

// isFile reports whether t binds a multipart part: File, *File, []File or []*File.
func isFile(t reflect.Type) bool {
	if t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t == fileType
}

// formFieldName is the form key a field binds, or "" when it is not a form
// field. A file part with no form: tag binds under its JSON name.
func formFieldName(f reflect.StructField) string {
	if tag, ok := f.Tag.Lookup("form"); ok {
		if name := jsontag.Name(f.Name, tag); name != "-" {
			return name
		}
		return ""
	}
	if isFile(f.Type) {
		return jsonFieldName(f)
	}
	return ""
}

// cookieFieldName is the request cookie a field reads, or "".
func cookieFieldName(f reflect.StructField) string {
	if tag, ok := f.Tag.Lookup("cookie"); ok {
		if name := jsontag.Name(f.Name, tag); name != "-" {
			return name
		}
	}
	return ""
}

// input is one request as the invoke seam takes it.
//
// sent says body is a REST request body exactly as it arrived, under media,
// which a [Body] field takes whole and a form or a [Parser] reads by its media.
// Otherwise body is the whole input as one value — an argument object over
// MCP and the CLI, a ZAP message over the call plane — and dec decodes it.
type input struct {
	dec   decoder
	body  []byte
	sent  bool
	media string
	// form reads the request's form fields and parts. REST only.
	form   func() (formData, error)
	query  map[string]string
	path   map[string]string
	header func(string) string
	cookie func(string) string
}

// formData is a request's form: its fields and its file parts by name.
type formData struct {
	values map[string][]string
	files  map[string][]File
}

// decode reads the body into v (a *In) the way the op's request kind says.
// consumes is what the op declared it takes.
func (r intake) decode(v any, in input, consumes []string) error {
	if !in.sent {
		return r.decodeArgs(v, in)
	}
	switch {
	case r.raw != nil:
		field := reflect.ValueOf(v).Elem().FieldByIndex(r.raw)
		field.Set(reflect.ValueOf(Body{Type: in.media, Bytes: append([]byte(nil), in.body...)}))
		return nil
	case r.parse:
		return parsed(v.(Parser).Parse(in.media, in.body))
	case r.form && isFormMedia(in.media):
		fd, err := in.form()
		if err != nil {
			return ErrBadRequest("invalid body: " + err.Error())
		}
		return bindForm(v, fd)
	case r.form:
		// A form op reads JSON only when it says it takes JSON; it then reads
		// the same fields under the same names, so one object binds whichever
		// way it was sent. A body in any other media binds nothing, as a form
		// reader finds no fields in it.
		if !isJSONMedia(in.media) || !takesJSON(consumes) {
			return nil
		}
		return r.decodeArgs(v, input{dec: jsonenc.Unmarshal, body: in.body, media: mimeJSON})
	}
	if len(in.body) == 0 {
		return nil
	}
	if err := in.dec(in.body, v); err != nil {
		return ErrBadRequest("invalid body: " + err.Error())
	}
	return nil
}

// decodeArgs reads an input that arrived as one value.
func (r intake) decodeArgs(v any, in input) error {
	if len(in.body) > 0 {
		if r.parse && in.media == mimeJSON {
			if err := parsed(v.(Parser).Parse(mimeJSON, in.body)); err != nil {
				return err
			}
		} else if err := in.dec(in.body, v); err != nil {
			return ErrBadRequest("invalid body: " + err.Error())
		}
		if r.form && in.media == mimeJSON {
			if err := bindFormArgs(v, in.body); err != nil {
				return ErrBadRequest("invalid body: " + err.Error())
			}
		}
	}
	return nil
}

// parsed is a Parser's error as the seam answers it.
func parsed(err error) error {
	if err == nil {
		return nil
	}
	var he *HTTPError
	if errors.As(err, &he) {
		return he
	}
	return ErrBadRequest("invalid body: " + err.Error())
}

// isFormMedia reports whether a Content-Type is a form encoding.
func isFormMedia(media string) bool {
	m, _, _ := mime.ParseMediaType(media)
	return m == mimeForm || m == mimeMultipart
}

// isJSONMedia reports whether a Content-Type is JSON: application/json or a
// +json suffix.
func isJSONMedia(media string) bool {
	m, _, _ := mime.ParseMediaType(media)
	return m == mimeJSON || strings.HasSuffix(m, "+json")
}

// takesJSON reports whether a declared media list names JSON.
func takesJSON(consumes []string) bool {
	for _, m := range consumes {
		if isJSONMedia(m) {
			return true
		}
	}
	return false
}

// bindForm writes a request's form onto v's form fields and file parts.
func bindForm(v any, fd formData) error {
	rv := reflect.ValueOf(v).Elem()
	for _, f := range wireFields(rv.Type()) {
		name := formFieldName(f)
		if name == "" {
			continue
		}
		fv := rv.FieldByIndex(f.Index)
		if isFile(f.Type) {
			setFiles(fv, fd.files[name])
			continue
		}
		setValues(fv, fd.values[name])
	}
	return nil
}

// setValues writes a form field's values: each repeated key one element of a
// list, else the first value.
func setValues(fv reflect.Value, values []string) {
	if len(values) == 0 {
		return
	}
	if fv.Kind() == reflect.Slice && fv.Type().Elem().Kind() != reflect.Uint8 && !readsText(fv.Type()) {
		out := reflect.MakeSlice(fv.Type(), len(values), len(values))
		for i, s := range values {
			setScalar(out.Index(i), s)
		}
		fv.Set(out)
		return
	}
	setScalar(fv, values[0])
}

// setFiles writes the parts of one name onto a File, *File, []File or []*File.
func setFiles(fv reflect.Value, parts []File) {
	if len(parts) == 0 {
		return
	}
	one := func(t reflect.Type, p File) reflect.Value {
		if t.Kind() == reflect.Pointer {
			c := p
			return reflect.ValueOf(&c)
		}
		return reflect.ValueOf(p)
	}
	if fv.Kind() == reflect.Slice {
		out := reflect.MakeSlice(fv.Type(), len(parts), len(parts))
		for i, p := range parts {
			out.Index(i).Set(one(fv.Type().Elem(), p))
		}
		fv.Set(out)
		return
	}
	fv.Set(one(fv.Type(), parts[0]))
}

// bindFormArgs writes an argument object's form fields onto v, read under
// their form names: an argument object names a form field the way the form
// does, so one name reaches it over REST, MCP and the CLI.
func bindFormArgs(v any, body []byte) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return err
	}
	rv := reflect.ValueOf(v).Elem()
	for _, f := range wireFields(rv.Type()) {
		name := formFieldName(f)
		raw, ok := obj[name]
		if name == "" || !ok || string(raw) == "null" {
			continue
		}
		fv := rv.FieldByIndex(f.Index)
		if isFile(f.Type) {
			if err := json.Unmarshal(raw, fv.Addr().Interface()); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			continue
		}
		values, err := argValues(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		setValues(fv, values)
	}
	return nil
}

// argValues reads one argument as the strings a form would carry: a list
// element by element, a string unquoted, any other value as written.
func argValues(raw json.RawMessage) ([]string, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) > 0 && raw[0] == '[' {
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, err
		}
		out := make([]string, 0, len(items))
		for _, it := range items {
			v, err := argValues(it)
			if err != nil {
				return nil, err
			}
			out = append(out, v...)
		}
		return out, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []string{s}, nil
	}
	return []string{string(raw)}, nil
}

// clearCookies zeroes v's cookie: fields, so the request's cookie is the only
// thing that sets one: not the body, not an argument object, not the URL. A
// page on another site can make a browser send a body; it cannot make it send
// a cookie it does not hold.
func clearCookies(v any) {
	rv := reflect.ValueOf(v).Elem()
	for _, f := range wireFields(rv.Type()) {
		if cookieFieldName(f) != "" {
			fv := rv.FieldByIndex(f.Index)
			fv.Set(reflect.Zero(fv.Type()))
		}
	}
}

// bindCookies copies declared cookie values onto the input, as bindHeaders does
// for headers.
func bindCookies(in any, get func(string) string) {
	if get == nil {
		return
	}
	v := reflect.ValueOf(in).Elem()
	for _, f := range wireFields(v.Type()) {
		name := cookieFieldName(f)
		if name == "" {
			continue
		}
		if got := get(name); got != "" {
			setScalar(v.FieldByIndex(f.Index), got)
		}
	}
}

// defaultBodyType fills a [Body] field's Type, when an argument object left it
// empty, with the first media the op consumes.
func (r intake) defaultBodyType(v any, media []string) {
	if r.raw == nil || len(media) == 0 {
		return
	}
	field := reflect.ValueOf(v).Elem().FieldByIndex(r.raw)
	if b := field.Addr().Interface().(*Body); b.Type == "" {
		b.Type = media[0]
	}
}

// base64Text is how bytes travel inside JSON.
func base64Text(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// content is a REST request's body with its Content-Encoding undone, each
// coding in turn and the result held to the app's BodyLimit, for an op that
// takes the body itself.
//
// It does not read through fiber's Body, which answers a coding it cannot undo
// with the error's text in place of the bytes (and sometimes a status on the
// response): a handler that takes the bytes as they are would run on that
// text. Here a coding this server does not undo is 415, a body that decodes
// past the limit is 413, and one that does not decode is 400, each before the
// handler runs. The bytes are the op's own: the decoders allocate, and an
// unencoded body is fiber's copy.
func content(c fiber.Ctx) ([]byte, error) {
	coding := strings.TrimSpace(string(c.Request().Header.ContentEncoding()))
	if coding == "" {
		return c.Body(), nil
	}
	limit := c.App().Config().BodyLimit
	body := c.Request().Body()
	// The last coding listed was applied last (RFC 9110 §8.4), so it is undone
	// first.
	codings := strings.Split(strings.ToLower(coding), ",")
	for i := len(codings) - 1; i >= 0; i-- {
		var err error
		if body, err = undo(strings.TrimSpace(codings[i]), body, limit); err != nil {
			return nil, err
		}
	}
	return append([]byte(nil), body...), nil
}

// undo removes one content coding from b, held to limit bytes.
func undo(coding string, b []byte, limit int) ([]byte, error) {
	var r fasthttp.Request
	r.SetBodyRaw(b)
	var (
		out []byte
		err error
	)
	switch coding {
	case "identity":
		return b, nil
	case "gzip", "x-gzip":
		out, err = r.BodyGunzipWithLimit(limit)
	case "deflate":
		out, err = r.BodyInflateWithLimit(limit)
	case "br":
		out, err = r.BodyUnbrotliWithLimit(limit)
	case "zstd":
		out, err = r.BodyUnzstdWithLimit(limit)
	default:
		return nil, Errorf(http.StatusUnsupportedMediaType, "Content-Encoding %q is not one this server undoes; send gzip, deflate, br, zstd or none", coding)
	}
	switch {
	case errors.Is(err, fasthttp.ErrBodyTooLarge):
		return nil, Errorf(http.StatusRequestEntityTooLarge, "the request body decodes to more than %d bytes", limit)
	case err != nil:
		return nil, ErrBadRequest(fmt.Sprintf("invalid body: its %s coding does not decode: %v", coding, err))
	}
	return out, nil
}

// readForm reads a REST request's form from its content: url-encoded fields,
// or a multipart form's fields and parts, each part read whole. The content is
// already decoded and held to the body limit, and a multipart form is parsed
// from it in memory, so no part is larger than the body that carried it.
func readForm(c fiber.Ctx, media string, body []byte) (formData, error) {
	fd := formData{values: map[string][]string{}, files: map[string][]File{}}
	if m, _, _ := mime.ParseMediaType(media); m != mimeMultipart {
		var args fasthttp.Args
		args.ParseBytes(body)
		for k, v := range args.All() {
			fd.values[string(k)] = append(fd.values[string(k)], string(v))
		}
		return fd, nil
	}
	boundary := string(c.Request().Header.MultipartFormBoundary())
	if boundary == "" {
		return fd, errors.New("a multipart form names no boundary")
	}
	form, err := multipart.NewReader(bytes.NewReader(body), boundary).ReadForm(int64(len(body)) + 1)
	if err != nil {
		return fd, err
	}
	defer func() { _ = form.RemoveAll() }()
	for k, v := range form.Value {
		fd.values[k] = v
	}
	for k, parts := range form.File {
		for _, h := range parts {
			f, err := h.Open()
			if err != nil {
				return fd, err
			}
			b, err := io.ReadAll(f)
			_ = f.Close()
			if err != nil {
				return fd, err
			}
			fd.files[k] = append(fd.files[k], File{Name: h.Filename, Type: h.Header.Get("Content-Type"), Bytes: b})
		}
	}
	return fd, nil
}
