package notify

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingSender struct{ got []Message }

func (r *recordingSender) Send(_ context.Context, m Message) error {
	r.got = append(r.got, m)
	return nil
}

func TestNotifier(t *testing.T) {
	tests := []struct {
		name        string
		job         Job
		wantSubject string
		wantBody    string
		wantErr     error
	}{
		{
			name:        "payment succeeded",
			job:         Job{ID: 9, Kind: KindPaymentSucceeded, Payload: []byte(`{"payment_id":4,"user_id":2,"amount_cents":1999,"currency":"usd"}`)},
			wantSubject: "Payment received",
			wantBody:    "We received your payment #4 of 19.99 usd.",
		},
		{
			name:        "asset allocated",
			job:         Job{ID: 9, Kind: KindAssetAllocated, Payload: []byte(`{"asset_id":7,"asset_tag":"L-102","user_id":2}`)},
			wantSubject: "Asset allocated to you",
			wantBody:    "Asset L-102 (#7) has been allocated to you.",
		},
		{name: "unknown kind", job: Job{Kind: "fax"}, wantErr: ErrPermanent},
		{name: "malformed payload", job: Job{Kind: KindAssetAllocated, Payload: []byte(`{`)}, wantErr: ErrPermanent},
		{name: "missing user", job: Job{Kind: KindPaymentSucceeded, Payload: []byte(`{"payment_id":4}`)}, wantErr: ErrPermanent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender := &recordingSender{}
			err := NewNotifier(sender).Handle(context.Background(), tt.job)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Empty(t, sender.got)
				return
			}
			require.NoError(t, err)
			require.Len(t, sender.got, 1)
			assert.Equal(t, int64(2), sender.got[0].UserID)
			assert.Equal(t, tt.wantSubject, sender.got[0].Subject)
			assert.Equal(t, tt.wantBody, sender.got[0].Body)
			assert.Equal(t, "job-9", sender.got[0].Key, "stable key so the provider can dedupe retries")
		})
	}
}
