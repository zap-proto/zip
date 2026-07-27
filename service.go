package zip

import "fmt"

// Service is a unit of functionality that attaches itself to an app: its
// routes, its middleware, its shutdown hooks. One type covers every way a unit
// can be composed, which is what makes where it runs a deployment decision
// instead of a code change:
//
//	app.Add(
//	    billing.New(deps),                                   // linked into this binary
//	    zip.Load("/v1/search", zip.Plugin{Name: "search",     // its own binary, embedded
//	        Bin: searchBin}),
//	    zip.Load("/v1/ml", zip.Plugin{Name: "ml",             // already running elsewhere
//	        Addr: os.Getenv("ML_ADDR")}),
//	)
//
// A constructor that takes dependencies and returns a Service is the same
// thing curried — func(deps) Service — so a unit's dependencies are bound
// where they are known and the composition root only ever sees Service.
type Service func(*App) error

// Add composes services into the app in order, stopping at the first error and
// naming which position failed. Order matters exactly as much as it does for
// hand-written route registration: earlier routes are registered earlier, and
// specificity still decides which one answers.
func (a *App) Add(services ...Service) error {
	for i, s := range services {
		if s == nil {
			continue
		}
		if err := s(a); err != nil {
			return fmt.Errorf("zip: Add service %d: %w", i, err)
		}
	}
	return nil
}
