// Package shopify is the adapter to Shopify: the OAuth handshake, the Admin API
// client, and signature verification.
//
// This package does not know *why* a quantity is being changed. It only knows
// how to talk to Shopify — and how to prove that the party talking back is
// actually Shopify.
package shopify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"sort"
	"strings"
)

// VerifyQuery checks the signature on an OAuth request (install and callback).
//
// The query string is walked raw, **not through url.Values**, and that is not
// stylistic: Go's Encode() re-encodes with its own rules — spaces become `+`,
// some punctuation gets escaped — so what would be hashed is no longer the text
// Shopify signed. A perfectly valid signature would be rejected, and whoever
// chased it would conclude the secret was wrong.
func VerifyQuery(rawQuery, secret string) bool {
	var given string
	var kept []string

	for _, pair := range strings.Split(rawQuery, "&") {
		if value, found := strings.CutPrefix(pair, "hmac="); found {
			given = value
			continue
		}
		kept = append(kept, pair)
	}
	if given == "" {
		return false
	}

	sort.Strings(kept)
	sum := sign([]byte(strings.Join(kept, "&")), secret)

	return hmac.Equal([]byte(hex.EncodeToString(sum)), []byte(given))
}

// VerifyWebhook checks the signature on a webhook body.
//
// Its form differs from OAuth's — base64 over the raw body, not hex over a
// query string — and that is an easy thing to get wrong: a single
// implementation used for both will always reject one of them.
//
// The body must be the one that has **not** been decoded. JSON re-marshalled
// can differ by a single space from what Shopify sent, and one space is enough
// to make the signature stop matching.
func VerifyWebhook(body []byte, header, secret string) bool {
	if header == "" {
		return false
	}
	sum := sign(body, secret)

	return hmac.Equal([]byte(base64.StdEncoding.EncodeToString(sum)), []byte(header))
}

func sign(message []byte, secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(message)
	return mac.Sum(nil)
}
