package mailer

import (
	"bufio"
	"context"
	"net"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestValidateRefusesByName(t *testing.T) {
	good := Config{Host: "smtp.example.com", Port: 587, Security: SecurityStartTLS, From: "kubemg@example.com"}
	if err := good.Validate(); err != nil {
		t.Fatalf("a good relay was refused: %v", err)
	}
	cases := map[string]Config{
		"no host":         {Port: 587, Security: SecurityStartTLS, From: "a@b.c"},
		"scheme":          {Host: "smtp://x", Port: 587, Security: SecurityStartTLS, From: "a@b.c"},
		"host with port":  {Host: "x.example.com:25", Port: 25, Security: SecurityStartTLS, From: "a@b.c"},
		"port":            {Host: "x", Port: 0, Security: SecurityStartTLS, From: "a@b.c"},
		"security":        {Host: "x", Port: 25, Security: "ssl", From: "a@b.c"},
		"sender":          {Host: "x", Port: 25, Security: SecurityStartTLS, From: "not an address"},
		"sender list":     {Host: "x", Port: 25, Security: SecurityStartTLS, From: "a@b.c\r\nBcc: e@f.g"},
		"plain with auth": {Host: "smtp.example.com", Port: 25, Security: SecurityNone, Username: "u", From: "a@b.c"},
	}
	for name, config := range cases {
		if err := config.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	local := Config{Host: "localhost", Port: 25, Security: SecurityNone, Username: "u", From: "a@b.c"}
	if err := local.Validate(); err != nil {
		t.Fatalf("a login to a relay on this host was refused: %v", err)
	}
}

func TestComposeKeepsTextOutOfHeaders(t *testing.T) {
	from, _ := mail.ParseAddress("KubeMG <kubemg@example.com>")
	to, _ := mail.ParseAddress("dev@example.com")
	if _, err := compose(from, to, "x\r\nBcc: evil@example.com", "body", time.Now()); err == nil {
		t.Fatal("a subject with a line break was composed")
	}
	data, err := compose(from, to, "Parolanız 3 gün içinde doluyor", "line one\nline two", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "Subject: =?utf-8?q?") {
		t.Fatalf("a non-ASCII subject was not encoded:\n%s", text)
	}
	if !strings.Contains(text, "line one\r\nline two") {
		t.Fatalf("body line endings not normalised:\n%s", text)
	}
}

// fakeRelay answers just enough SMTP for one message and hands back the DATA.
func fakeRelay(t *testing.T, extensions []string) (int, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	got := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		reader := bufio.NewReader(conn)
		write := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
		write("220 fake ESMTP")
		var data strings.Builder
		inData := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				got <- data.String()
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					write("250 queued")
					continue
				}
				data.WriteString(line)
				continue
			}
			verb := strings.ToUpper(strings.Fields(line + " x")[0])
			switch verb {
			case "EHLO":
				lines := append([]string{"fake"}, extensions...)
				for i, ext := range lines {
					sep := "-"
					if i == len(lines)-1 {
						sep = " "
					}
					write("250" + sep + ext)
				}
			case "MAIL", "RCPT", "RSET", "NOOP":
				write("250 ok")
			case "DATA":
				inData = true
				write("354 go ahead")
			case "QUIT":
				write("221 bye")
				got <- data.String()
				return
			default:
				write("502 not implemented")
			}
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port, got
}

func TestSendDeliversThroughARelay(t *testing.T) {
	port, got := fakeRelay(t, nil)
	config := Config{Host: "127.0.0.1", Port: port, Security: SecurityNone, From: "kubemg@example.com"}
	err := Send(context.Background(), config, Message{To: "dev@example.com", Subject: "Hello", Body: "the body"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	data := <-got
	if !strings.Contains(data, "Subject: Hello") || !strings.Contains(data, "the body") {
		t.Fatalf("relay received:\n%s", data)
	}
}

func TestStartTLSIsNeverFallenBackFrom(t *testing.T) {
	port, _ := fakeRelay(t, nil)
	config := Config{Host: "127.0.0.1", Port: port, Security: SecurityStartTLS, From: "kubemg@example.com"}
	err := Send(context.Background(), config, Message{To: "dev@example.com", Subject: "s", Body: "b"})
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("a relay without STARTTLS was used: %v", err)
	}
}

func TestSendRefusesABadRecipient(t *testing.T) {
	config := Config{Host: "127.0.0.1", Port: 1, Security: SecurityNone, From: "kubemg@example.com"}
	err := Send(context.Background(), config, Message{To: "a@b.c\r\nBcc: x@y.z", Subject: "s", Body: "b"})
	if err == nil || !strings.Contains(err.Error(), "recipient") {
		t.Fatalf("got %v", err)
	}
}
