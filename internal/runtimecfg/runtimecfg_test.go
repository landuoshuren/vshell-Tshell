package runtimecfg

import "testing"

func TestAppendParse(t *testing.T) {
	want := Config{ServerAddr: "127.0.0.1:8084", Vkey: "key", VerifyKey: "verify", Transport: "tcp", ProxyURL: "socks5://127.0.0.1:1080"}
	data, err := Append([]byte("program"), want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("config mismatch: %#v != %#v", got, want)
	}
	encoded, err := Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err = Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("encoded config mismatch: %#v != %#v", got, want)
	}
}

func TestParseRejectsPlainProgram(t *testing.T) {
	if _, err := Parse([]byte("program")); err == nil {
		t.Fatal("expected missing configuration error")
	}
}
