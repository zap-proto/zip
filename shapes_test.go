// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/zap-proto/zip"
)

// Prose written once reaches every field it describes: a promoted field carries
// the comment its embedded struct wrote, and an instantiated generic is
// published under a name a schema may have while its prose stays filed under
// the name zipdoc wrote.

// Record is what every record carries.
type Record struct {
	ID string `json:"id"`
}

type purchase struct {
	Record
	Total int64 `json:"total"`
}

type envelope[T any] struct {
	Status string `json:"status"`
	Data   T      `json:"data"`
}

func TestShapes_ProseReachesPromotedAndGenericFields(t *testing.T) {
	app := zip.New(zip.Config{AppName: "shapes", DisableStartupMessage: true})
	zip.Describe("GET /v1/orders/:id", zip.Doc{
		Description: "Get returns one purchase.",
		Fields: map[string]string{
			"Record.id":      "ID addresses the record.",
			"purchase.total": "Total is in cents.",
			"envelope[github.com/zap-proto/zip_test.purchase].status": "Status is ok or error.",
			"envelope[github.com/zap-proto/zip_test.purchase].data":   "Data is the purchase.",
			"reportIn.id": "ID names the purchase.",
		},
	})
	app.Get("/v1/orders/:id", func(_ context.Context, in *reportIn) (*envelope[purchase], error) {
		return &envelope[purchase]{Status: "ok"}, nil
	})
	b, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	env, ok := doc.Components.Schemas["envelopePurchase"]
	if !ok {
		t.Fatalf("no envelopePurchase in %s", b)
	}
	if env.Properties["status"].Description != "Status is ok or error." || env.Properties["data"].Description != "Data is the purchase." {
		t.Errorf("envelopePurchase = %+v", env)
	}
	o := doc.Components.Schemas["purchase"]
	if o.Properties["id"].Description != "ID addresses the record." || o.Properties["total"].Description != "Total is in cents." {
		t.Errorf("purchase = %+v, want the promoted id described where Record declares it", o)
	}
}
