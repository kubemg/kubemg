package auth

import (
	"strings"
	"testing"

	saml2 "github.com/russellhaering/gosaml2"
	"github.com/russellhaering/gosaml2/types"
)

func assertionAnswering(inResponseTo string) *saml2.AssertionInfo {
	return &saml2.AssertionInfo{Assertions: []types.Assertion{{
		Subject: &types.Subject{SubjectConfirmation: &types.SubjectConfirmation{
			Method:                  "urn:oasis:names:tc:SAML:2.0:cm:bearer",
			SubjectConfirmationData: &types.SubjectConfirmationData{InResponseTo: inResponseTo},
		}},
	}}}
}

// A SAML assertion is accepted only as the answer to the request this sign-in
// sent. A valid response captured from another sign-in, posted against a fresh
// state inside its validity window, answers a different request and is
// refused; so is one that does not say, and there is no unsolicited sign-in.
func TestASAMLAssertionMustAnswerThisSignInsRequest(t *testing.T) {
	if err := answersRequest(assertionAnswering("id-abc"), "id-abc"); err != nil {
		t.Fatalf("the assertion answering this request: %v", err)
	}
	for name, tc := range map[string]struct {
		info      *saml2.AssertionInfo
		requestID string
		want      string
	}{
		"another request":     {assertionAnswering("id-other"), "id-abc", "different sign-in"},
		"no InResponseTo":     {assertionAnswering(""), "id-abc", "does not say"},
		"no confirmation":     {&saml2.AssertionInfo{Assertions: []types.Assertion{{Subject: &types.Subject{}}}}, "id-abc", "does not say"},
		"no assertion":        {&saml2.AssertionInfo{}, "id-abc", "no assertion"},
		"no request was sent": {assertionAnswering("id-abc"), "", "sent no SAML request"},
	} {
		err := answersRequest(tc.info, tc.requestID)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want an error naming %q, got %v", name, tc.want, err)
		}
	}
}
