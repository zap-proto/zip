package zip

import "fmt"

// What the thing IS, declared where the thing is.
//
// [Doc] carries what one OPERATION means. This carries what the PACKAGE means —
// the product a customer buys, where it sits on the menu, what shape it is, and
// whether it belongs on the menu at all. Those are facts about a subsystem, not
// about any one of its routes, and until now they had nowhere to live but a
// hand-kept table in whatever service assembles the catalog: a second list of
// every product, in another repo, free to disagree with the code and — measured —
// doing so.
//
// It travels the same road the prose does and for the same reason. Go drops
// comments at compile time, so cmd/zipdoc reads the PACKAGE doc comment at build
// time and emits a [Catalog] call beside the [Describe] calls. The declaration is
// therefore compiled INTO the binary that serves the subsystem, which is the whole
// difference: a reader that instead resolves an import path to a directory and
// reads the source off disk can only describe packages that live in ITS OWN repo,
// and every subsystem shipped as its own module reads as undeclared. Three of them
// did.
//
//	// Package vector is similarity search over your own embeddings.
//	//
//	// Product:    Hanzo Vector
//	// Category:   data
//	// Kind:       api
//	// Visibility: public
//	// Meters:     per-GB-month
//	// Backup:     sqlite:/data/vector.db retention=30d
//	package vector
//
// The x- names are the wire spelling, and they are on the TYPE rather than on
// each carrier of it, because a document embeds this value in more than one place
// — an app's `info`, a product's `tag` — and two spellings of one fact is how a
// consumer ends up reading whichever it happened to parse.

// public is the one Visibility that puts a product on the menu. Named once so the
// reading ([Meta.Public]) and the vocabulary ([Meta.Valid]) cannot drift apart.
const public = "public"

// noBackup is the one word of the Backup vocabulary this grammar knows: the
// EXPLICIT statement that there is nothing to capture. The rest of the
// vocabulary — stores, retention, delegation — belongs to the service that runs
// backups, exactly as Category's words belong to the catalog. zip knows "none"
// only because zip owns the difference between saying it and not saying it.
const noBackup = "none"

// Meta is what a package says about the product it implements.
//
// OMISSION HIDES. Every field is optional and the zero value is the honest
// answer for a package that has declared nothing — except Visibility, where the
// zero value is not neutral but INTERNAL. That asymmetry is the point: a
// subsystem's health probe, its dev bridge, its install hook and its provider
// flags are all real operations that no customer buys, and a default of "public"
// puts every one of them on the menu the day the package is written. A default of
// "internal" costs one line in the packages that are products and leaks nothing in
// the ones that are not.
type Meta struct {
	// Description is the package's own one-sentence synopsis, lifted verbatim.
	// It is what the subsystem says it is; Product is what it is SOLD as, and
	// the two are different sentences about the same thing.
	Description string `json:"description,omitempty"`

	// Product is what a customer buys — "Hanzo Vector", not "vector" and not
	// "provisioning". The implementing package's name is an implementation
	// fact and is regularly not the name on the invoice; a menu built from
	// package names sells things nobody has heard of.
	Product string `json:"x-product,omitempty"`

	// Category is the section of the menu this sits under. The vocabulary is
	// the catalog's, not this package's — zip carries the value and the service
	// that owns the taxonomy is the one that may refuse a word in it.
	Category string `json:"x-category,omitempty"`

	// Kind is what SHAPE the product is. Not everything a customer buys is an
	// endpoint: some are deployed on their own footing and some run entirely on
	// the customer's device. Reading every product as an API makes each of the
	// others look like a missing one, which is a gap report about a thing that
	// was built. Same rule as Category — the words belong to the catalog.
	Kind string `json:"x-kind,omitempty"`

	// Visibility is "public" or "internal", and ABSENT MEANS INTERNAL. See the
	// type's own note: this is the one field whose omission is a decision.
	Visibility string `json:"x-visibility,omitempty"`

	// Meters is how usage is charged, in the billing system's own words.
	Meters string `json:"x-meters,omitempty"`

	// Backup is what has to be captured for this subsystem to be restorable,
	// stated by the code that owns the data rather than by whoever writes the
	// runbook.
	//
	// FOR A PRODUCT, SILENCE IS REFUSED ([Meta.Valid]). "Backup: none" is a
	// reviewed decision that there is nothing to capture; an absent line is
	// nobody deciding, and nobody-deciding is how a product runs unprotected
	// until the day of the restore. Same rule as the inert-middleware refusal:
	// the difference between "off" and "never wired" must not read alike.
	Backup string `json:"x-backup,omitempty"`
}

// Public reports whether the product belongs on the customer-facing menu.
//
// ONE function, so the default lives in one place. Every caller that instead
// tested `Visibility != "internal"` would put an undeclared package on the menu,
// which is exactly the leak the default exists to prevent.
func (m Meta) Public() bool { return m.Visibility == public }

// BackedUp reports whether the product declares data that is captured.
//
// ONE function, like [Meta.Public], so "declared none" and "declared nothing"
// cannot be conflated by a caller: a dashboard that instead tested
// `Backup != ""` would show a product green for having SAID "none".
func (m Meta) BackedUp() bool { return m.Backup != "" && m.Backup != noBackup }

// Valid refuses a Visibility this grammar does not know.
//
// Only Visibility, and only because its default HIDES: "Public", "pubic" and
// "yes" all read as internal, so a typo would silently withhold a product that
// was meant to be sold and nothing anywhere would say so. An unknown Category or
// Kind is loud wherever the taxonomy is — the value reaches the document and the
// service that owns the vocabulary refuses it there. A wrong Visibility reaches
// nothing.
func (m Meta) Valid() error {
	switch m.Visibility {
	case "", "internal", public:
	default:
		return fmt.Errorf("Visibility: %q is neither %q nor \"internal\"; an unrecognised value hides the product silently, "+
			"so it is refused here instead", m.Visibility, public)
	}
	// A PRODUCT with no Backup line fails the build. This is the second field
	// whose omission would be a silent decision: an unstated backup posture
	// reads as "not backed up" to the machinery and as "surely somebody backs
	// it up" to everyone else, and the two readings meet at a restore. A
	// product must say either what is captured or, explicitly, "none".
	if m.Product != "" && m.Backup == "" {
		return fmt.Errorf("Product %q declares no Backup: a product must state its backup posture — what has to be captured, "+
			"or explicitly \"Backup: none\" — because silence is how a product runs unprotected until the day of the restore", m.Product)
	}
	return nil
}

// catalog is the process-wide declaration, keyed by IMPORT PATH — the one name a
// package has that is stable across repos, modules and vendoring, and the same
// name a composition root already writes when it imports the subsystem it mounts.
var catalog = map[string]Meta{}

// Catalog records what the package at import path pkg says about its product.
// Generated code calls it from an init(); a hand-written call is possible and
// defeats the point, since the doc comment is then no longer the single source.
func Catalog(pkg string, m Meta) { catalog[pkg] = m }

// Cataloged is the declaration for an import path, and whether there is one.
//
// The distinction is load-bearing for the caller that maps a binary to its
// product: "this package declared nothing" and "this package is not linked into
// me" must not read alike, or a composition root silently describes the wrong
// subsystem.
func Cataloged(pkg string) (Meta, bool) {
	m, ok := catalog[pkg]
	return m, ok
}

// Catalogs is every declaration linked into this process, as a copy.
//
// It exists for the projections that need the WHOLE set rather than one entry —
// a backup planner deriving what to capture, a menu deriving what to sell. They
// read the same registry the per-package lookup reads, so there is no second
// list for a projection to fall out of step with.
func Catalogs() map[string]Meta {
	out := make(map[string]Meta, len(catalog))
	for k, v := range catalog {
		out[k] = v
	}
	return out
}
