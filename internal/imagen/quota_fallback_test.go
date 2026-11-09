package imagen

import "testing"

func TestSafeRejectionGatewayQuotaErrors(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"insufficient_user_quota","message":"Not enough quota to pre-consume for this request"}}`,
		`{"error":{"message":"用户额度不足, 剩余额度: ¥-0.001560"}}`,
		`{"error":{"message":"insufficient balance"}}`,
	} {
		if !safeRejection(403, []byte(body)) {
			t.Fatalf("quota rejection not eligible for fallback: %s", body)
		}
	}
	if safeRejection(403, []byte(`{"error":{"message":"forbidden"}}`)) {
		t.Fatal("generic 403 became fallback-eligible")
	}
}
