package zip_test

import "github.com/zap-proto/zip"

// must unwraps a definition constructor's (*App, error) so it can be composed
// inline: app.Use(must(zip.Load(...))).
//
// zip.Load and zip.Mount return an error because BUILDING a definition can fail
// — a binary that will not start, a scheme with no transport. Use cannot fail,
// because appending a reference to a program is not a fallible act. The error
// belongs at construction, and composition stays one verb.
func must(p *zip.App, err error) *zip.App {
	if err != nil {
		panic(err)
	}
	return p
}
