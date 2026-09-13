package email

import (
	"context"
	"errors"
	"net/smtp"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/domain"
)

func TestConsoleSender_RecordsSentMessages(t *testing.T) {
	s := NewConsoleSender(zap.NewNop())

	msg := domain.EmailMessage{To: "fan@example.com", Subject: "Your ticket", Body: "Thanks for your order."}
	if err := s.Send(context.Background(), msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sent := s.Sent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sent))
	}
	if sent[0] != msg {
		t.Errorf("expected recorded message to match what was sent, got %+v", sent[0])
	}
}

func TestConsoleSender_RecordsMultipleMessagesInOrder(t *testing.T) {
	s := NewConsoleSender(zap.NewNop())

	for i := 0; i < 3; i++ {
		_ = s.Send(context.Background(), domain.EmailMessage{To: "fan@example.com", Subject: "msg", Body: string(rune('a' + i))})
	}

	sent := s.Sent()
	if len(sent) != 3 {
		t.Fatalf("expected 3 sent messages, got %d", len(sent))
	}
	if sent[0].Body != "a" || sent[2].Body != "c" {
		t.Errorf("expected messages recorded in send order, got bodies %q, %q, %q", sent[0].Body, sent[1].Body, sent[2].Body)
	}
}

func TestSMTPSender_BuildsCorrectMessageAndCallsSendMail(t *testing.T) {
	var capturedAddr, capturedFrom string
	var capturedTo []string
	var capturedBody string

	s := NewSMTPSender("smtp.example.com", "587", "user", "pass", "noreply@ticketing.example")
	s.sendMailFunc = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		capturedAddr, capturedFrom, capturedTo, capturedBody = addr, from, to, string(msg)
		return nil
	}

	err := s.Send(context.Background(), domain.EmailMessage{
		To: "fan@example.com", Subject: "Your ticket for Gor Mahia vs AFC Leopards", Body: "See you at the match!",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedAddr != "smtp.example.com:587" {
		t.Errorf("expected addr 'smtp.example.com:587', got %q", capturedAddr)
	}
	if capturedFrom != "noreply@ticketing.example" {
		t.Errorf("expected from 'noreply@ticketing.example', got %q", capturedFrom)
	}
	if len(capturedTo) != 1 || capturedTo[0] != "fan@example.com" {
		t.Errorf("expected to=['fan@example.com'], got %v", capturedTo)
	}
	if !strings.Contains(capturedBody, "Subject: Your ticket for Gor Mahia vs AFC Leopards") {
		t.Error("expected the built message to contain the Subject header")
	}
	if !strings.Contains(capturedBody, "See you at the match!") {
		t.Error("expected the built message to contain the body text")
	}
}

func TestSMTPSender_PropagatesTransportError(t *testing.T) {
	s := NewSMTPSender("smtp.example.com", "587", "user", "pass", "noreply@ticketing.example")
	s.sendMailFunc = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		return errors.New("connection refused")
	}

	err := s.Send(context.Background(), domain.EmailMessage{To: "fan@example.com", Subject: "x", Body: "y"})
	if err == nil {
		t.Fatal("expected an error to propagate from the underlying transport")
	}
}
