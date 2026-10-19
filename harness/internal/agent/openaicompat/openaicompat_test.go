package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"harness/internal/agent"
)

// fakeServer answers every request with reply and records what it was sent.
func fakeServer(t *testing.T, reply string) (*httptest.Server, *map[string]any) {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func tinyPNG(t *testing.T) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 160, 144))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestToolCall(t *testing.T) {
	srv, sent := fakeServer(t, `{"choices":[{"message":{"content":"","tool_calls":[
		{"function":{"name":"press","arguments":"{\"buttons\":[\"UP\",\"a\"],\"reasoning\":\"door is north\"}"}}]}}]}`)
	a := New(Config{BaseURL: srv.URL + "/v1", Model: "m", Vision: true})

	act, err := a.Decide(context.Background(), agent.Observation{Step: 3, Screen: tinyPNG(t), StateText: "Map: Pallet Town"})
	if err != nil {
		t.Fatal(err)
	}
	if act.Tool != "press" || string(act.Args) != `{"buttons":["up","a"]}` || act.Reasoning != "door is north" {
		t.Errorf("action = %+v (args %s)", act, act.Args)
	}

	// With vision on, the user message is text plus an image part.
	msgs := (*sent)["messages"].([]any)
	parts := msgs[1].(map[string]any)["content"].([]any)
	if len(parts) != 2 || parts[1].(map[string]any)["type"] != "image_url" {
		t.Errorf("user content = %v", parts)
	}
	if !strings.Contains(parts[0].(map[string]any)["text"].(string), "Map: Pallet Town") {
		t.Errorf("state text missing from prompt")
	}
}

func TestJSONFallback(t *testing.T) {
	// A model that ignores tools and writes JSON after some thinking.
	srv, sent := fakeServer(t, `{"choices":[{"message":{"content":"I should talk to mom. {\"buttons\":[\"a\"]}"}}]}`)
	a := New(Config{BaseURL: srv.URL + "/v1", Model: "m"})

	act, err := a.Decide(context.Background(), agent.Observation{Step: 1})
	if err != nil {
		t.Fatal(err)
	}
	if string(act.Args) != `{"buttons":["a"]}` {
		t.Errorf("args = %s", act.Args)
	}
	// With vision off, the user message is plain text.
	msgs := (*sent)["messages"].([]any)
	if _, ok := msgs[1].(map[string]any)["content"].(string); !ok {
		t.Errorf("text-only content should be a string, got %T", msgs[1].(map[string]any)["content"])
	}
}

func TestRejectsBadButtons(t *testing.T) {
	srv, _ := fakeServer(t, `{"choices":[{"message":{"tool_calls":[
		{"function":{"name":"press","arguments":"{\"buttons\":[\"jump\"]}"}}]}}]}`)
	a := New(Config{BaseURL: srv.URL + "/v1", Model: "m"})
	if _, err := a.Decide(context.Background(), agent.Observation{}); err == nil || !strings.Contains(err.Error(), "jump") {
		t.Errorf("err = %v, want unknown button", err)
	}
}

func TestServerError(t *testing.T) {
	srv, _ := fakeServer(t, `{"error":{"message":"model not loaded"}}`)
	a := New(Config{BaseURL: srv.URL + "/v1", Model: "m"})
	if _, err := a.Decide(context.Background(), agent.Observation{}); err == nil || !strings.Contains(err.Error(), "model not loaded") {
		t.Errorf("err = %v", err)
	}
}
