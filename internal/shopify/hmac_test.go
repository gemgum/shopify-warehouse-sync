package shopify

import "testing"

// The official example from Shopify's OAuth documentation. Used verbatim so
// this test fails if the calculation drifts — not merely if it changes.
const (
	sampleSecret = "hush"
	sampleQuery  = "code=0907a61c0c8d55e99db179b68161bc00&" +
		"hmac=700e2dadb827fcc8609e9d5ce208b2e9cdaab9df07390d2cbca10d7c328fc4bf&" +
		"shop=some-shop.myshopify.com&state=0.6784241404160823&timestamp=1337178173"
)

func TestVerifyQuery(t *testing.T) {
	if !VerifyQuery(sampleQuery, sampleSecret) {
		t.Error("a valid signature was rejected")
	}
	if VerifyQuery(sampleQuery, "the-wrong-secret") {
		t.Error("the wrong secret was accepted")
	}
	if VerifyQuery("shop=some-shop.myshopify.com&timestamp=1337178173", sampleSecret) {
		t.Error("a request with no hmac was accepted")
	}
	// Parameter order must not matter: Shopify does not promise one, and what
	// gets hashed is the sorted list.
	shuffled := "state=0.6784241404160823&shop=some-shop.myshopify.com&" +
		"hmac=700e2dadb827fcc8609e9d5ce208b2e9cdaab9df07390d2cbca10d7c328fc4bf&" +
		"timestamp=1337178173&code=0907a61c0c8d55e99db179b68161bc00"
	if !VerifyQuery(shuffled, sampleSecret) {
		t.Error("a different parameter order was rejected")
	}
}

func TestVerifyWebhook(t *testing.T) {
	body := []byte(`{"inventory_item_id":123,"available":7}`)
	// base64(hmac-sha256(body, "hush"))
	const signature = "KtAZeNyNvl1pJWq2e+6eZdkGPWtOvpT9+AvBQ+uxlr4="

	if !VerifyWebhook(body, signature, sampleSecret) {
		t.Error("a valid webhook signature was rejected")
	}
	// A body that differs by one byte must be rejected — that is the whole
	// point of the check.
	if VerifyWebhook([]byte(`{"inventory_item_id":123,"available":8}`), signature, sampleSecret) {
		t.Error("a tampered body was accepted")
	}
	if VerifyWebhook(body, "", sampleSecret) {
		t.Error("a webhook with no signature header was accepted")
	}
}
