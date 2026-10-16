package media

import "net/url"

// hostOf is the host[:port] of an http(s) base URL.
func hostOf(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", errBadBaseURL
	}
	return u.Host, nil
}

// errBadBaseURL: a base URL that is not an http(s) URL with a host.
var errBadBaseURL = errString("the base URL is not an http(s) URL with a host")

type errString string

func (e errString) Error() string { return string(e) }

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
