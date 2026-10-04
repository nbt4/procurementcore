package api

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestPrivateAdamHallContextBoundEncryptedAndRedacted(t *testing.T) {
	t.Setenv("CORES_JWT_SECRET", strings.Repeat("private-checkout-key-", 3))
	record := adamHallCheckoutRecord{ID: 17, PurchaseOrderID: 5, UserID: 42, LocalContext: strings.Repeat("a", 64)}
	secret := "private-authenticated-supplier-context"
	ciphertext, err := encryptAdamHallContext(record, secret)
	if err != nil || bytes.Contains(ciphertext, []byte(secret)) {
		t.Fatal("private supplier context stored in plaintext", err)
	}
	record.ContextCipher = ciphertext
	decoded, err := decryptAdamHallContext(record)
	if err != nil || decoded != secret {
		t.Fatal("private checkout restoration", err)
	}
	raw, _ := json.Marshal(record)
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "context_cipher") {
		t.Fatal("private context exposed by public record", string(raw))
	}
	for _, change := range []func(*adamHallCheckoutRecord){func(r *adamHallCheckoutRecord) { r.ID++ }, func(r *adamHallCheckoutRecord) { r.PurchaseOrderID++ }, func(r *adamHallCheckoutRecord) { r.UserID++ }, func(r *adamHallCheckoutRecord) { r.LocalContext = strings.Repeat("b", 64) }} {
		other := record
		change(&other)
		if _, err := decryptAdamHallContext(other); err == nil {
			t.Fatal("private context relabelled or reused")
		}
	}
	record.ContextCipher = append([]byte(nil), ciphertext...)
	record.ContextCipher[len(record.ContextCipher)-1] ^= 1
	if _, err := decryptAdamHallContext(record); err == nil {
		t.Fatal("altered private context accepted")
	}
	t.Setenv("CORES_JWT_SECRET", strings.Repeat("rotated-checkout-key-", 3))
	record.ContextCipher = ciphertext
	if _, err := decryptAdamHallContext(record); err == nil {
		t.Fatal("supplier context survived signing key rotation")
	}
}
