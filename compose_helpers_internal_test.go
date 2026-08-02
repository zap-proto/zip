package zip

// mustApp unwraps a definition constructor's (*App, error) for an in-package
// test. See must in compose_helpers_test.go for why the error lives at
// construction and not at Use.
func mustApp(a *App, err error) *App {
	if err != nil {
		panic(err)
	}
	return a
}
