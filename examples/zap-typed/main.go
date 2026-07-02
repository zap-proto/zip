// zap-typed example — typed handler with auto-generated OpenAPI spec.
//
//	go run ./examples/zap-typed
//	curl -X POST http://localhost:8080/v1/validate \
//	    -H 'Content-Type: application/json' \
//	    -d '{"email":"z@hanzo.ai","age":30}'
//	curl http://localhost:8080/.well-known/openapi.json | jq
//	open http://localhost:8080/docs
package main

import (
	"context"
	"log"
	"strings"

	"github.com/zap-proto/zip"
)

// ValidateRequest is the input type. zip derives an OpenAPI schema
// from these struct tags.
type ValidateRequest struct {
	Email string `json:"email" validate:"required,minlen=3,maxlen=255"`
	Age   int    `json:"age" validate:"required,min=18,max=120"`
}

// ValidateResponse is the output type. Same auto-schema derivation.
type ValidateResponse struct {
	OK         bool   `json:"ok"`
	Normalized string `json:"normalized"`
}

func main() {
	app := zip.New(zip.Config{
		AppName: "zap-typed",
		OpenAPI: zip.OpenAPIConfig{
			Title:       "Validate API",
			Description: "Example of zip's typed handler + auto-OpenAPI",
			Version:     "v0.1.0",
		},
	})

	zip.Post(app, "/v1/validate", validate,
		zip.WithSummary("Validate an email and age"),
		zip.WithTags("validation"),
	)

	log.Fatal(app.ListenHTTP(":8080"))
}

func validate(ctx context.Context, in *ValidateRequest) (*ValidateResponse, error) {
	_ = ctx
	return &ValidateResponse{
		OK:         true,
		Normalized: strings.ToLower(in.Email),
	}, nil
}
