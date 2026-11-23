// Package webui embeds the single-page application assets.
package webui

import "embed"

// FS contains the web UI files served by the HTTP layer.
//
//go:embed index.html favicon.svg css js
var FS embed.FS
