// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip

// What the package's own tests may reach that a caller may not.
//
// buildOpenAPIReflect is the document derived straight from the Go types, and
// it is the ORACLE: the shipped document is derived from the manifest instead,
// and the two must be the same bytes or the manifest lost something. It cannot
// be exported for real — a service has one document, not two ways of asking for
// it — and the test that compares them lives outside this package because the
// corpus it compares over imports it.

// OpenAPIByReflection is buildOpenAPIReflect, for project_corpus_test.go.
func OpenAPIByReflection(a *App) map[string]any { return a.buildOpenAPIReflect() }

// GraphQLByReflection is graphQLByReflection, for project_corpus_test.go.
func GraphQLByReflection(a *App) string { return a.graphQLByReflection() }
