// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/zap-proto/zip/internal/jsontag"
)

// The held answer's prose is the comments on Approval's fields, stated once
// more for the document because no zipdoc pass reads zip's own source. This
// holds the two to one text.
func TestApprovalProseIsItsComments(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "authorize.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "Approval" {
			return true
		}
		for _, field := range ts.Type.(*ast.StructType).Fields.List {
			tag := reflect.StructTag(strings.Trim(field.Tag.Value, "`")).Get("json")
			want["Approval."+jsontag.Name(field.Names[0].Name, tag)] = strings.TrimSpace(field.Doc.Text())
		}
		return false
	})
	if !reflect.DeepEqual(want, approvalProse) {
		t.Errorf("approvalProse = %v\nthe comments say %v", approvalProse, want)
	}
}
