package blobstore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const emptySHA256Hex = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

type sigV4Credential struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Region          string
	Service         string
}

func signV4(req *http.Request, credential sigV4Credential, payloadHash string, now time.Time) error {
	if req.URL == nil || req.URL.Host == "" {
		return fmt.Errorf("sigv4 request URL host required")
	}
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("X-Amz-Date", amzDate)
	if credential.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", credential.SessionToken)
	}

	signedHeaders := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	if credential.SessionToken != "" {
		signedHeaders = append(signedHeaders, "x-amz-security-token")
	}
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalPath(req),
		req.URL.Query().Encode(),
		canonicalHeaders(req, signedHeaders),
		strings.Join(signedHeaders, ";"),
		payloadHash,
	}, "\n")
	scope := date + "/" + credential.Region + "/" + credential.Service + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")
	sig := hmacSHA256(signingKey(credential.SecretAccessKey, date, credential.Region, credential.Service), []byte(stringToSign))
	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		credential.AccessKeyID,
		scope,
		strings.Join(signedHeaders, ";"),
		hex.EncodeToString(sig),
	))
	return nil
}

func canonicalPath(req *http.Request) string {
	path := req.URL.EscapedPath()
	if path == "" {
		return "/"
	}
	return path
}

func canonicalHeaders(req *http.Request, names []string) string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	var b strings.Builder
	for _, name := range sorted {
		value := req.Header.Get(name)
		if name == "host" {
			value = req.URL.Host
		}
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(strings.Join(strings.Fields(value), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

func signingKey(secret, date, region, service string) []byte {
	dateKey := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	regionKey := hmacSHA256(dateKey, []byte(region))
	serviceKey := hmacSHA256(regionKey, []byte(service))
	return hmacSHA256(serviceKey, []byte("aws4_request"))
}

func hmacSHA256(key []byte, msg []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)
	return mac.Sum(nil)
}

func hexSHA256(msg []byte) string {
	sum := sha256.Sum256(msg)
	return hex.EncodeToString(sum[:])
}

// unsignedPayload is the payload hash of a presigned request: the signer
// cannot know the body a later client sends.
const unsignedPayload = "UNSIGNED-PAYLOAD"

// presignV4 returns u (whose path is already AWS-URI-encoded in RawPath, or
// plain in Path) as a SigV4 query-signed URL valid for expires from now.
// Only the host header is signed; temporary credentials add
// X-Amz-Security-Token to the signed query (the S3 query-auth rules). Any
// query parameters already on u (e.g. response-content-disposition) are
// signed too.
func presignV4(method string, u *url.URL, credential sigV4Credential, now time.Time, expires time.Duration) (string, error) {
	if u == nil || u.Host == "" {
		return "", fmt.Errorf("sigv4 presign URL host required")
	}
	secs := int64(expires / time.Second)
	if secs < 1 || secs > 7*24*3600 {
		return "", fmt.Errorf("sigv4 presign expiry must be 1s..7d")
	}
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	scope := date + "/" + credential.Region + "/" + credential.Service + "/aws4_request"
	q := u.Query()
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", credential.AccessKeyID+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", fmt.Sprint(secs))
	q.Set("X-Amz-SignedHeaders", "host")
	if credential.SessionToken != "" {
		q.Set("X-Amz-Security-Token", credential.SessionToken)
	}
	query := canonicalQuery(q)
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonicalRequest := strings.Join([]string{
		method,
		path,
		query,
		"host:" + u.Host + "\n",
		"host",
		unsignedPayload,
	}, "\n")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")
	sig := hmacSHA256(signingKey(credential.SecretAccessKey, date, credential.Region, credential.Service), []byte(stringToSign))
	out := *u
	out.RawQuery = query + "&X-Amz-Signature=" + hex.EncodeToString(sig)
	return out.String(), nil
}

// canonicalQuery is the SigV4 canonical query string: names and values
// AWS-URI-encoded, sorted by name then value, joined with "&". The same
// string is sent on the wire, so what S3 decodes is exactly what was signed.
func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := append([]string(nil), q[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, awsURIEncode(k, true)+"="+awsURIEncode(v, true))
		}
	}
	return strings.Join(parts, "&")
}

// awsURIEncode percent-encodes every byte except the RFC 3986 unreserved
// set (A-Z a-z 0-9 - _ . ~), upper-case hex, as SigV4 requires; "/" is kept
// only when encodeSlash is false (object key paths).
func awsURIEncode(s string, encodeSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&15])
		}
	}
	return b.String()
}
