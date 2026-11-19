package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/descope/virtualwebauthn"
)

// Real clients return extension outputs such as credProps whether or not the RP
// asked. go-webauthn 0.18 rejects them by default, which would lock out passkey
// registration, login and recovery; NewWebAuthn sets UnsolicitedOutputPolicyIgnore
// (#0130). This drives a full registration whose attestation carries credProps.
func TestFullCeremony_UnsolicitedExtensionOutputAccepted(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	svc := newService(t, pool, &recordingMailer{}, "")

	const email = "ext@example.com"
	ctx := context.Background()
	if err := svc.StartRegistration(ctx, email, ""); err != nil {
		t.Fatal(err)
	}
	token := lastPendingToken(t, pool, email)
	creation, err := svc.VerifyRegistration(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	rp := virtualwebauthn.RelyingParty{ID: testRPID, Name: "ShortLinks", Origin: testRPOrigin}
	optionsJSON, _ := json.Marshal(creation)
	attOpts, err := virtualwebauthn.ParseAttestationOptions(string(optionsJSON))
	if err != nil {
		t.Fatal(err)
	}
	resp := virtualwebauthn.CreateAttestationResponse(rp, virtualwebauthn.NewAuthenticator(),
		virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2), *attOpts)

	var m map[string]any
	if err := json.Unmarshal([]byte(resp), &m); err != nil {
		t.Fatal(err)
	}
	m["clientExtensionResults"] = map[string]any{"credProps": map[string]any{"rk": true}}
	body, _ := json.Marshal(m)

	req := httptest.NewRequest(http.MethodPost, "/auth/register/finish?token="+token, bytes.NewReader(body))
	if _, err := svc.FinishRegistration(ctx, token, "Key", "", req); err != nil {
		t.Fatalf("FinishRegistration with an unsolicited credProps output: %v", err)
	}
}
