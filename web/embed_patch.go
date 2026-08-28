package main

import "bytes"

func init() {
	const marker = "</body>"
	injected := []byte("<script src=\"/optimization.js\"></script>\n</body>")
	autoHTML = bytes.Replace(autoHTML, []byte(marker), injected, 1)
}
