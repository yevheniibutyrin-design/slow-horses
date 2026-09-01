// Package docs embeds the OpenAPI specification so the API can serve it.
package docs

import _ "embed"

// OpenAPI is the raw contents of openapi.yaml.
//
//go:embed openapi.yaml
var OpenAPI []byte
