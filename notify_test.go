package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// fakeSMTP speaks just enough SMTP to accept one message, so the client
// side is exercised for real: greeting, MAIL, RCPT, DATA.
func fakeSMTP(t *testing.T) (addr string, got chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	got = make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r, w := bufio.NewReader(conn), bufio.NewWriter(conn)
		say := func(s string) { w.WriteString(s + "\r\n"); w.Flush() }
		say("220 fake ESMTP")
		var b strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if strings.TrimRight(line, "\r\n") == "." {
					inData = false
					say("250 Ok")
					got <- b.String()
					continue
				}
				b.WriteString(line)
				continue
			}
			raw := strings.TrimSpace(line)
			cmd := strings.ToUpper(raw)
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				say("250-fake")
				say("250 AUTH PLAIN")
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"),
				strings.HasPrefix(cmd, "AUTH"):
				b.WriteString(raw + "\n")
				say("250 Ok")
			case strings.HasPrefix(cmd, "DATA"):
				inData = true
				say("354 Go ahead")
			case strings.HasPrefix(cmd, "QUIT"):
				say("221 Bye")
				return
			default:
				say("250 Ok")
			}
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String(), got
}

func TestSMTPChannel(t *testing.T) {
	addr, got := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(addr)
	c := Channel{Kind: ChanSMTP, Name: "mail", Host: host, Port: atoiOr(port), Security: "none",
		From: "smokestack@example.net", To: "noc@example.net, astreinte@example.net"}
	if err := checkChannel(&c); err != nil {
		t.Fatal(err)
	}
	if err := c.Send("[smokestack] Transit down", "Body of the alert\n"); err != nil {
		t.Fatalf("send: %v", err)
	}
	msg := <-got
	for _, want := range []string{"MAIL FROM:<smokestack@example.net>", "RCPT TO:<noc@example.net>",
		"RCPT TO:<astreinte@example.net>", "Subject: [smokestack] Transit down", "Body of the alert"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the SMTP session lacks %q:\n%s", want, msg)
		}
	}
}

func atoiOr(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

// Every HTTP provider: the payload shape and the credentials are what a
// provider rejects silently, so they are checked here.
func TestHTTPChannels(t *testing.T) {
	type capture struct {
		path, auth, body, ovhSig string
	}
	var last capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		last = capture{r.URL.Path, r.Header.Get("Authorization"), string(b), r.Header.Get("X-Ovh-Signature")}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	cases := []struct {
		name  string
		c     Channel
		check func(t *testing.T)
	}{
		{"webhook", Channel{Kind: ChanWebhook, URL: srv.URL}, func(t *testing.T) {
			var m map[string]any
			json.Unmarshal([]byte(last.body), &m)
			if m["subject"] != "Subj" || !strings.Contains(m["body"].(string), "Body") {
				t.Errorf("webhook payload: %s", last.body)
			}
		}},
		{"slack", Channel{Kind: ChanSlack, URL: srv.URL}, func(t *testing.T) {
			if !strings.Contains(last.body, `"text"`) || !strings.Contains(last.body, "Subj") {
				t.Errorf("slack payload: %s", last.body)
			}
		}},
		{"teams", Channel{Kind: ChanTeams, URL: srv.URL}, func(t *testing.T) {
			if !strings.Contains(last.body, "MessageCard") || !strings.Contains(last.body, "Subj") {
				t.Errorf("teams payload: %s", last.body)
			}
		}},
		{"twilio", Channel{Kind: ChanTwilio, AccountSID: "AC123", AuthToken: "secret",
			FromNumber: "+33100000000", ToNumbers: "+33600000000", Endpoint: srv.URL}, func(t *testing.T) {
			if !strings.Contains(last.path, "/2010-04-01/Accounts/AC123/Messages.json") {
				t.Errorf("twilio path: %s", last.path)
			}
			if last.auth == "" || !strings.Contains(last.body, "To=%2B33600000000") {
				t.Errorf("twilio call: %s / %s", last.auth, last.body)
			}
		}},
		{"gatewayapi", Channel{Kind: ChanGateway, Token: "tok", ToNumbers: "+33600000000",
			Endpoint: srv.URL}, func(t *testing.T) {
			if last.auth == "" || !strings.Contains(last.body, `"msisdn":"33600000000"`) {
				t.Errorf("gatewayapi call: %s / %s", last.auth, last.body)
			}
		}},
		{"ovh_sms", Channel{Kind: ChanOVHSMS, AppKey: "ak", AppSecret: "as", ConsumerKey: "ck",
			ServiceName: "sms-ab1234-1", ToNumbers: "+33600000000", Endpoint: srv.URL}, func(t *testing.T) {
			if !strings.HasPrefix(last.ovhSig, "$1$") {
				t.Errorf("the OVH request must be signed: %q", last.ovhSig)
			}
			if !strings.Contains(last.path, "/sms/sms-ab1234-1/jobs") ||
				!strings.Contains(last.body, "+33600000000") {
				t.Errorf("ovh call: %s / %s", last.path, last.body)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ch := c.c
			ch.Name = c.name
			if err := checkChannel(&ch); err != nil {
				t.Fatalf("refused: %v", err)
			}
			if err := ch.Send("Subj", "Body of the alert\nSince: now\n"); err != nil {
				t.Fatalf("send: %v", err)
			}
			c.check(t)
		})
	}
}

// A provider answering with an error must say so, not fail silently.
func TestChannelReportsProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"error":"invalid token"}`))
	}))
	defer srv.Close()
	c := Channel{Kind: ChanSlack, Name: "slack", URL: srv.URL, Enabled: true}
	err := c.Send("Subj", "Body")
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "invalid token") {
		t.Errorf("the provider's answer must be reported: %v", err)
	}
	// And one broken channel must not stop the others.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ok.Close()
	set := ChannelSet{Channels: []Channel{c, {Kind: ChanWebhook, Name: "hook", URL: ok.URL, Enabled: true}}}
	err = SendAll(set, "Subj", "Body")
	if err == nil || !strings.Contains(err.Error(), "1 channel(s) delivered") {
		t.Errorf("SendAll should deliver what it can and report the rest: %v", err)
	}
}

func TestChannelChecksAndShortText(t *testing.T) {
	bad := []Channel{
		{Kind: ChanSMTP, Host: "", From: "a@b.fr", To: "c@d.fr"},
		{Kind: ChanSlack, URL: "not-a-url"},
		{Kind: ChanTelegram, Token: "x"},
		{Kind: ChanTwilio, AccountSID: "AC1"},
		{Kind: "carrier-pigeon"},
	}
	for _, c := range bad {
		if err := checkChannel(&c); err == nil {
			t.Errorf("%v should have been refused", c.Kind)
		}
	}
	smtpCh := Channel{Kind: ChanSMTP, Host: "mail.example.net", From: "a@b.fr", To: "c@d.fr"}
	if err := checkChannel(&smtpCh); err != nil || smtpCh.Port != 587 || smtpCh.Security != "starttls" {
		t.Errorf("sensible SMTP defaults expected: %+v %v", smtpCh, err)
	}
	short := shortText("[smokestack] Transit down for 7 min",
		"Transit (192.0.2.1) has been in incident for 7 min.\n\nState: 42.0 % packet loss\n"+
			"Traceroute taken during the incident:\n   1  192.0.2.254\nGraph: https://x/t/y\n", 160)
	if strings.Contains(short, "192.0.2.254") || !strings.Contains(short, "42.0 %") || len(short) > 160 {
		t.Errorf("a phone-sized message was expected: %q", short)
	}
}

// A target's title reaches the Subject header of every alert, and an editor
// can set a title while only an admin may configure a channel. A title
// carrying a newline therefore let the lesser role add headers of its own to
// mail the greater role had set up — a Bcc, for instance.
func TestSubjectCannotAddHeaders(t *testing.T) {
	addr, got := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(addr)
	p, _ := strconv.Atoi(port)
	c := Channel{Kind: ChanSMTP, Host: host, Port: p, Security: "none",
		From: "noc@example.net", To: "ops@example.net"}
	subject := "[smokestack] Router\r\nBcc: attacker@example.com\r\nX-Injected: yes down for 5 min"
	if err := c.Send(subject, "body"); err != nil {
		t.Fatalf("send: %v", err)
	}
	msg := <-got
	// Line by line, and not a substring search: the point is that "Bcc:" is
	// no longer the start of a header, not that the three characters are
	// gone. Folding the newline into a space leaves the text on the Subject
	// line, which is the right outcome — the operator still reads it.
	var headers []string
	for _, line := range strings.Split(msg, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			break // the blank line ends the header block
		}
		headers = append(headers, line)
	}
	for _, h := range headers {
		for _, forbidden := range []string{"Bcc:", "X-Injected:"} {
			if strings.HasPrefix(h, forbidden) {
				t.Errorf("the subject added the header %q:\n%s", h, msg)
			}
		}
	}
	subjects := 0
	for _, h := range headers {
		if strings.HasPrefix(h, "Subject: ") {
			subjects++
		}
	}
	if subjects != 1 {
		t.Errorf("want exactly one Subject header, got %d:\n%s", subjects, msg)
	}
	// The text survives, on one line: refusing the newline must not throw
	// away what the operator is meant to read.
	if !strings.Contains(msg, "Router") || !strings.Contains(msg, "down for 5 min") {
		t.Errorf("the subject lost its text:\n%s", msg)
	}
}

// An address handed to a command is an argument. sendmail reads one that
// starts with "-" as an option, so a recipient can otherwise be a debug flag
// that writes a trace file.
func TestMailAddressesRefuseArgumentsAndHeaders(t *testing.T) {
	for _, c := range []struct {
		name, from, to string
		ok             bool
	}{
		{"plain", "noc@example.net", "ops@example.net", true},
		{"several", "noc@example.net", "a@example.net, b@example.net", true},
		{"recipient is an option", "noc@example.net", "-X/tmp/trace@example.net", false},
		{"sender is an option", "-X/tmp/trace@example.net", "ops@example.net", false},
		{"newline in a recipient", "noc@example.net", "ops@example.net\r\nBcc: x@y.net", false},
		{"newline in the sender", "noc@example.net\r\nBcc: x@y.net", "ops@example.net", false},
		{"angle brackets", "noc@example.net", "<ops@example.net>", false},
		{"no recipient", "noc@example.net", "   ", false},
		{"not an address", "noc@example.net", "ops", false},
	} {
		_, err := checkMailAddresses("sendmail", c.from, c.to)
		if (err == nil) != c.ok {
			t.Errorf("%s: from=%q to=%q -> err=%v, want ok=%v", c.name, c.from, c.to, err, c.ok)
		}
	}
}

// The directory a mail command lives in is not the same question as which
// command it is. Pinning the full path breaks an operator whose system puts
// it elsewhere; leaving it free makes the setting an arbitrary exec.
func TestMailCommandIsAMailCommandAnywhere(t *testing.T) {
	for _, c := range []struct {
		path string
		ok   bool
	}{
		{"/usr/sbin/sendmail", true},
		{"/usr/lib/sendmail", true},
		{"/usr/local/sbin/sendmail", true}, // FreeBSD
		{"/nix/store/abc123-postfix-3.9.0/bin/sendmail", true},
		{"/usr/bin/msmtp", true},
		{"/usr/bin/mailx", true},

		{"/bin/sh", false},
		{"/usr/bin/curl", false},
		{"sendmail", false},                   // relative
		{"/usr/sbin/../../bin/sh", false},     // not cleaned
		{"/usr/sbin/sendmail\nrm -rf", false}, // not a plain path
		{"", false},
	} {
		err := checkMailCommand(c.path)
		if (err == nil) != c.ok {
			t.Errorf("%q -> err=%v, want ok=%v", c.path, err, c.ok)
		}
	}
	// And the channel check applies it, with the documented default.
	ch := Channel{Kind: ChanSendmail, From: "noc@example.net", To: "ops@example.net"}
	if err := checkChannel(&ch); err != nil || ch.Command != "/usr/sbin/sendmail" {
		t.Errorf("default sendmail path expected: %q %v", ch.Command, err)
	}
	ch.Command = "/bin/sh"
	if err := checkChannel(&ch); err == nil {
		t.Error("a channel pointing at /bin/sh must be refused")
	}
}
