package account

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// A pod that has just started can find the database unreachable for a moment; the server used to exit on the first failed ping and come back only through a container restart. Open now keeps trying until the caller's deadline, and the error it finally returns carries the cause instead of hiding it.
func TestOpenRetriesUntilTheDeadlineAndKeepsTheCause(t *testing.T) {
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := closed.Addr().String()
	closed.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Open(ctx, "postgres://synthetic:synthetic-password@"+addr+"/synthetic?sslmode=disable")
	if err == nil {
		t.Fatal("connected to a port nothing listens on")
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Fatalf("gave up after %v, before the deadline", elapsed)
	}
	var dial *net.OpError
	if !errors.As(err, &dial) {
		t.Fatalf("the cause was not kept: %v", err)
	}
	if strings.Contains(err.Error(), "synthetic-password") {
		t.Fatalf("the error names the password: %v", err)
	}
}

// The case the retry is for: the database only becomes reachable after Open has started trying.
func TestOpenWaitsForADatabaseThatComesUpLate(t *testing.T) {
	dsn := os.Getenv("MSIME_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("MSIME_TEST_DATABASE_URL is not set")
	}
	target, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := reserved.Addr().String()
	reserved.Close()
	upstream := target.Host
	late := make(chan net.Listener, 1)
	go func() {
		time.Sleep(time.Second)
		l, err := net.Listen("tcp", addr)
		if err != nil {
			late <- nil
			return
		}
		late <- l
		for {
			client, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer client.Close()
				server, err := net.Dial("tcp", upstream)
				if err != nil {
					return
				}
				defer server.Close()
				go io.Copy(server, client)
				io.Copy(client, server)
			}()
		}
	}()
	t.Cleanup(func() {
		if l := <-late; l != nil {
			l.Close()
		}
	})
	proxied := *target
	proxied.Host = addr
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := Open(ctx, proxied.String())
	if err != nil {
		t.Fatalf("did not wait for the database: %v", err)
	}
	db.Close()
}
