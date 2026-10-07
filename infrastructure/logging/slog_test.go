package logging

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func TestNewSlogHandler_Levels(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "slog.log")
	l, err := New(Options{Level: "debug", Filename: filename})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	slogger := NewSlogLogger(l.Logger())
	slogger.Debug("dbg")
	slogger.Info("inf")
	slogger.Warn("wrn")
	slogger.Error("err")

	data, _ := os.ReadFile(filename)
	s := string(data)
	for _, want := range []string{"dbg", "inf", "wrn", "err"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in log:\n%s", want, s)
		}
	}
}

func TestNewSlogHandler_AttrsAndGroup(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "slog2.log")
	l, err := New(Options{Level: "info", Filename: filename})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	h := NewSlogHandler(l.Logger()).WithGroup("grp").WithAttrs([]slog.Attr{slog.String("a", "1")})
	slogger := slog.New(h)
	slogger.Info("msg", slog.String("b", "2"))

	data, _ := os.ReadFile(filename)
	s := string(data)
	if !strings.Contains(s, "a=1") || !strings.Contains(s, "b=2") {
		t.Fatalf("attrs missing from log:\n%s", s)
	}
}

func TestNewSlogHandler_RequestIDFromContext(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "slog3.log")
	l, err := New(Options{Level: "info", Filename: filename})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	h := NewSlogHandler(l.Logger())
	ctx := WithRequestID(context.Background(), "req-9")
	if err := h.Handle(ctx, slog.NewRecord(time.Now(), slog.LevelInfo, "with id", 0)); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filename)
	if !strings.Contains(string(data), "req-9") {
		t.Fatalf("request_id not threaded:\n%s", data)
	}
}

func TestNewSlogHandler_UnknownLevel(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "slog4.log")
	l, err := New(Options{Level: "debug", Filename: filename})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	h := NewSlogHandler(l.Logger())
	// An unrecognized level falls through to the default info branch.
	if err := h.Handle(context.Background(), slog.NewRecord(time.Now(), slog.Level(99), "weird level", 0)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filename)
	if !strings.Contains(string(data), "weird level") {
		t.Fatalf("record not logged:\n%s", data)
	}
}

func TestNewSlogHandler_NestedGroup(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "slog5.log")
	l, err := New(Options{Level: "info", Filename: filename})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	h := NewSlogHandler(l.Logger()).WithGroup("a").WithGroup("b")
	slogger := slog.New(h)
	slogger.Info("nested")

	data, _ := os.ReadFile(filename)
	if !strings.Contains(string(data), "nested") {
		t.Fatalf("nested group record not logged:\n%s", data)
	}
}

func TestNewSlogHandler_Enabled(t *testing.T) {
	h := NewSlogHandler(logrus.New())
	if !h.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("handler should be enabled")
	}
}
