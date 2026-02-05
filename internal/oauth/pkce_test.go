package oauth

import (
	"testing"
)

func TestGenerateCodeVerifier(t *testing.T) {
	verifier, err := GenerateCodeVerifier()
	if err != nil {
		t.Fatalf("GenerateCodeVerifier() error = %v", err)
	}

	// RFC 7636: code verifier must be 43-128 characters
	if len(verifier) < 43 || len(verifier) > 128 {
		t.Errorf("GenerateCodeVerifier() length = %d, want 43-128", len(verifier))
	}

	// Generate another and verify they're different (random)
	verifier2, err := GenerateCodeVerifier()
	if err != nil {
		t.Fatalf("GenerateCodeVerifier() error = %v", err)
	}

	if verifier == verifier2 {
		t.Error("GenerateCodeVerifier() generated identical verifiers")
	}
}

func TestGenerateCodeChallenge(t *testing.T) {
	// Test vector from RFC 7636 Appendix B
	// Note: The RFC example uses a specific verifier that generates a known challenge
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	expectedChallenge := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

	challenge := GenerateCodeChallenge(verifier)
	if challenge != expectedChallenge {
		t.Errorf("GenerateCodeChallenge() = %v, want %v", challenge, expectedChallenge)
	}
}

func TestVerifyCodeChallenge_S256(t *testing.T) {
	verifier, err := GenerateCodeVerifier()
	if err != nil {
		t.Fatalf("GenerateCodeVerifier() error = %v", err)
	}

	challenge := GenerateCodeChallenge(verifier)

	// Valid verification
	if !VerifyCodeChallenge(verifier, challenge, "S256") {
		t.Error("VerifyCodeChallenge(S256) = false, want true for valid verifier")
	}

	// Invalid verifier
	if VerifyCodeChallenge("wrong-verifier", challenge, "S256") {
		t.Error("VerifyCodeChallenge(S256) = true, want false for invalid verifier")
	}

	// Invalid challenge
	if VerifyCodeChallenge(verifier, "wrong-challenge", "S256") {
		t.Error("VerifyCodeChallenge(S256) = true, want false for invalid challenge")
	}
}

func TestVerifyCodeChallenge_Plain(t *testing.T) {
	verifier := "my-plain-verifier"
	challenge := verifier // plain method: challenge = verifier

	// Valid verification
	if !VerifyCodeChallenge(verifier, challenge, "plain") {
		t.Error("VerifyCodeChallenge(plain) = false, want true for valid verifier")
	}

	// Invalid verifier
	if VerifyCodeChallenge("wrong-verifier", challenge, "plain") {
		t.Error("VerifyCodeChallenge(plain) = true, want false for invalid verifier")
	}
}

func TestVerifyCodeChallenge_UnsupportedMethod(t *testing.T) {
	if VerifyCodeChallenge("verifier", "challenge", "unknown") {
		t.Error("VerifyCodeChallenge(unknown) = true, want false for unsupported method")
	}
}
