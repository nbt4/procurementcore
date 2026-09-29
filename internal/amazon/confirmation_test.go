package amazon

import (
	"strings"
	"testing"
)

func confirmationXML(headerType, statuses string) []byte {
	return []byte(`<cXML payloadID="amazon-message-1"><Header><From><Credential domain="NetworkId"><Identity>Amazon</Identity></Credential></From><To><Credential domain="NetworkId"><Identity>buyer</Identity></Credential></To></Header><Request><ConfirmationRequest><ConfirmationHeader confirmID="303-1234567-1234567" type="` + headerType + `" operation="new" noticeDate="2026-09-29T12:00:00Z"/><OrderReference orderID="PO-1"><DocumentReference payloadID="outgoing-1"/></OrderReference>` + statuses + `</ConfirmationRequest></Request></cXML>`)
}

func TestParseConfirmation(t *testing.T) {
	items := `<ConfirmationItem lineNumber="1" quantity="2"><ConfirmationStatus type="detail" quantity="1" deliveryDate="2026-10-03T12:00:00Z"><Comments type="confirmID">303-1234567-1234567</Comments></ConfirmationStatus><ConfirmationStatus type="reject" quantity="1"/></ConfirmationItem>`
	c, err := ParseConfirmation(confirmationXML("detail", items), "buyer")
	if err != nil {
		t.Fatal(err)
	}
	if c.PurchaseOrder != "PO-1" || c.OrderPayloadID != "outgoing-1" || len(c.Lines) != 2 || c.Lines[0].Accepted != 1 || c.Lines[0].Delivery == nil || c.Lines[1].Rejected != 1 {
		t.Fatalf("unexpected parsed confirmation: %+v", c)
	}
	if _, err := ParseConfirmation(confirmationXML("reject", ""), "buyer"); err != nil {
		t.Fatalf("whole order rejection: %v", err)
	}
}

func TestParseConfirmationRejectsSpoofedOrInvalidMessages(t *testing.T) {
	valid := string(confirmationXML("detail", `<ConfirmationItem lineNumber="1" quantity="1"><ConfirmationStatus type="accept" quantity="1"/></ConfirmationItem>`))
	cases := []struct{ name, xml, buyer string }{
		{"wrong buyer", valid, "other"},
		{"wrong sender", strings.Replace(valid, "<Identity>Amazon</Identity>", "<Identity>Other</Identity>", 1), "buyer"},
		{"over quantity", strings.Replace(valid, `quantity="1"/></ConfirmationItem>`, `quantity="2"/></ConfirmationItem>`, 1), "buyer"},
		{"unknown status", strings.Replace(valid, `type="accept"`, `type="unknown"`, 1), "buyer"},
		{"missing order number", strings.ReplaceAll(valid, "303-1234567-1234567", ""), "buyer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseConfirmation([]byte(tc.xml), tc.buyer); err == nil {
				t.Fatal("unexpectedly accepted")
			}
		})
	}
}

func TestConfirmationBasicCredentials(t *testing.T) {
	client := &Client{cfg: Config{FromIdentity: "buyer", SharedSecret: "secret"}}
	if !client.VerifyConfirmationCredentials("buyer", "secret") || client.VerifyConfirmationCredentials("buyer", "bad") || client.VerifyConfirmationCredentials("other", "secret") {
		t.Fatal("basic credentials validation mismatch")
	}
}

func TestConfirmationCXMLCredentials(t *testing.T) {
	client := &Client{cfg: Config{FromIdentity: "buyer", SharedSecret: "secret"}}
	message := `<cXML><Header><To><Credential domain="NetworkID"><Identity>buyer</Identity></Credential></To><Sender><Credential domain="NetworkID"><Identity>Amazon</Identity><SharedSecret>secret</SharedSecret></Credential></Sender></Header></cXML>`
	if !client.VerifyConfirmationCXML([]byte(message)) {
		t.Fatal("valid cXML credentials rejected")
	}
	if client.VerifyConfirmationCXML([]byte(strings.Replace(message, "<SharedSecret>secret</SharedSecret>", "<SharedSecret>wrong</SharedSecret>", 1))) {
		t.Fatal("wrong secret accepted")
	}
	if client.VerifyConfirmationCXML([]byte(strings.Replace(message, "<Identity>buyer</Identity>", "<Identity>other</Identity>", 1))) {
		t.Fatal("wrong identity accepted")
	}
	if client.VerifyConfirmationCXML([]byte(strings.Replace(message, "<SharedSecret>secret</SharedSecret>", "", 1))) {
		t.Fatal("missing secret accepted")
	}
}
