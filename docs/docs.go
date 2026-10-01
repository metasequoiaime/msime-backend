// Package docs carries the documentation the msime-cloud command line prints: admin.md describes the admin API, including the request bodies the route tables do not.
package docs

import _ "embed"

//go:embed admin.md
var Admin string
