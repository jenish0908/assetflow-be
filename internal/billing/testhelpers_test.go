package billing

import "github.com/stripe/stripe-go/v81/webhook"

func signedHeader(payload []byte, secret string) string {
	return webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: secret}).Header
}
