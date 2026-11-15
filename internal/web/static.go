package web

import "embed"

//go:embed static/app.css static/app.js static/favicon.svg static/fonts
var staticFS embed.FS
