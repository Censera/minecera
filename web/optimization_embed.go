package main

import "bytes"

func init() {
	autoHTML = bytes.Replace(
		autoHTML,
		[]byte("</body>"),
		[]byte("<script src=\"/optimization.js\"></script></body>"),
		1,
	)
}
