// Package httpx holds small HTTP helpers shared by several services.
package httpx

import "mime"

// ContentDisposition formats a Content-Disposition naming fileName (RFC
// 6266/2231 encoding for non-ASCII names); just the disposition when the
// name is empty or unencodable.
func ContentDisposition(disposition, fileName string) string {
	if fileName != "" {
		if cd := mime.FormatMediaType(disposition, map[string]string{"filename": fileName}); cd != "" {
			return cd
		}
	}
	return disposition
}
