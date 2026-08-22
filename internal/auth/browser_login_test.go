package auth

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// STATE IS THE ONLY THING SEPARATING THIS SIGN-IN FROM ANY OTHER PAGE ON THE
// MACHINE. The listener is on localhost, so anything running here can reach it;
// without the check, a page could drive it into redeeming a code the person
// never asked for.
func TestCallbackRefusesTheWrongState(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = http.Get("http://" + ln.Addr().String() + "/callback?code=STOLEN&state=someone-elses")
	}()

	code, err := awaitCallback(context.Background(), ln, "ours")
	if err == nil {
		t.Fatal("a callback with the wrong state was accepted")
	}
	if code != "" {
		t.Fatalf("a code was taken from it anyway: %q", code)
	}
	if !strings.Contains(err.Error(), "state") {
		t.Fatalf("the reason does not name the cause: %v", err)
	}
}

// The happy path: the right state hands back the code and the browser is told
// it can go away.
func TestCallbackTakesTheCode(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// Kanal, paylaşılan değişken DEĞİL: testin kendisi yarış üretirse
	// ölçtüğü şey artık kod değil, testtir.
	bodies := make(chan string, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		res, err := http.Get("http://" + ln.Addr().String() + "/callback?code=GOOD&state=ours")
		if err != nil {
			bodies <- ""
			return
		}
		defer func() { _ = res.Body.Close() }()
		buf := make([]byte, 256)
		n, _ := res.Body.Read(buf)
		bodies <- string(buf[:n])
	}()

	code, err := awaitCallback(context.Background(), ln, "ours")
	if err != nil {
		t.Fatalf("awaitCallback: %v", err)
	}
	if code != "GOOD" {
		t.Fatalf("code: %q", code)
	}
	select {
	case body := <-bodies:
		if !strings.Contains(body, "Signed in") {
			t.Fatalf("the browser was left without a word: %q", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the browser never got a response")
	}
}

// An authorization the person refused must surface as their refusal, not as a
// timeout five minutes later.
func TestCallbackSurfacesTheServersError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = http.Get("http://" + ln.Addr().String() +
			"/callback?state=ours&error=access_denied&error_description=you+said+no")
	}()

	if _, err := awaitCallback(context.Background(), ln, "ours"); err == nil ||
		!strings.Contains(err.Error(), "you said no") {
		t.Fatalf("the refusal was lost: %v", err)
	}
}

// The loopback ports are not arbitrary — they are the redirect URIs seeded into
// the `palbase-cli` client. Binding one outside the list produces a code the
// authorization server refuses to deliver.
func TestLoopbackBindsARegisteredPort(t *testing.T) {
	ln, redirect, err := listenLoopback()
	if err != nil {
		t.Skipf("no callback port free on this machine: %v", err)
	}
	defer func() { _ = ln.Close() }()

	ok := false
	for _, p := range LoopbackCallbackPorts {
		if strings.Contains(redirect, ":"+itoa(p)+"/callback") {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("%s is not one of the registered redirect URIs %v", redirect, LoopbackCallbackPorts)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
