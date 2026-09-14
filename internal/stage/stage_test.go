package stage

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestRecoveredTemplatesMatchOriginalDifferentialOutputs(t *testing.T) {
	want := map[string]string{
		"windows_amd64.exe": "3BD8125B7460A12D4D9395E75F3C87F9CF1297922E0445A380FB6931C17509C4",
		"windows_i386.exe":  "D382250738EBAA9D676D302B40D70B6663365D427F887FEECA2E919C6015EAA3",
		"linux_amd64":       "44F3C47585E6F9FF8C0F131CAA5F8E30E7B75C024750E51BACC1CEA8E080D0BA",
		"linux_i386":        "C79076C2D5E038FEEFAC7FA5B571E428DBC01770B3AB24CCC13D4DF2DF34F044",
		"linux_arm64":       "1A5D5E6F425C7934D27481740F615B1F3077B93EAC4A30C8BBFFBDC706B297EC",
		"linux_arm":         "8A7933641CD64E6F7DE9F60A626FA21E92EF4742709938399E764F8C0077577F",
		"darwin_amd64":      "4FA0CA6AD5FC0A8ED9CED24C83CD0965332303312899C1FF10121F407AE3E10A",
		"darwin_arm64":      "48399F5E682E9B1520C0E065AF58D2D280D57C6EC3424BAF977D102D8527EB1E",
	}
	for arch, expected := range want {
		got, err := Launcher(arch, "10.20.30.40:23456")
		if err != nil {
			t.Fatalf("Launcher(%s): %v", arch, err)
		}
		assertHash(t, arch, got, expected)
	}
}

func TestRecoveredShellcodeMatchesOriginalOutputs(t *testing.T) {
	want := map[string]string{
		"windows_amd64": "A603F7BBC4F12B1909C1EF2E309F3201E1D27F93EA2BEB0056C1FD86499D8582",
		"windows_i386":  "C6173105D2C870662DEA5CB5A8929C6B1425866E46E79FDFE043E5751C815F0F",
	}
	for arch, expected := range want {
		got, _, err := Shellcode(arch, "10.20.30.40:23456", ".bin")
		if err != nil {
			t.Fatalf("Shellcode(%s): %v", arch, err)
		}
		assertHash(t, arch, got, expected)
	}

	c, _, err := Shellcode("windows_amd64", "127.0.0.1:19084", ".c")
	if err != nil {
		t.Fatal(err)
	}
	assertHash(t, "windows_amd64.c", c, "634F38C42F1088F7C016791E0895E8734B6A60FA34CB88F9B8929641A73B3C4E")
	raw, _, err := Shellcode("windows_amd64", "127.0.0.1:19084", ".raw.txt")
	if err != nil {
		t.Fatal(err)
	}
	assertHash(t, "windows_amd64.raw.txt", raw, "DA94C36C3888866F4779063AF9FFD886D56CB238560AAC84D5679E26DD90F561")
}

func assertHash(t *testing.T, name string, data []byte, expected string) {
	t.Helper()
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != stringLower(expected) {
		t.Fatalf("%s hash = %s, want %s", name, got, expected)
	}
}

func stringLower(v string) string {
	b := []byte(v)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
