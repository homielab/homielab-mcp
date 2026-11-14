package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testKey writes a fresh P-256 key as AuthKey_<id>.p8 into dir.
func testKey(t *testing.T, dir, id string) (*ecdsa.PrivateKey, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "AuthKey_"+id+".p8")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return k, p
}

// isolate points the config dir at a temp dir and clears ASC_* env vars.
func isolate(t *testing.T) string {
	t.Helper()
	for _, v := range []string{"ASC_KEY_ID", "ASC_ISSUER_ID", "ASC_PRIVATE_KEY", "ASC_PRIVATE_KEY_PATH", "ASC_VENDOR_NUMBER"} {
		t.Setenv(v, "")
	}
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	old := configBase
	configBase = t.TempDir()
	t.Cleanup(func() { configBase = old })
	dir, _ := configDir()
	return dir
}

func TestLoadCredentialsDiscovery(t *testing.T) {
	dir := isolate(t)
	if _, err := loadCredentials(); !errors.Is(err, errNoCredentials) || !strings.Contains(err.Error(), "setup --key-file") {
		t.Fatalf("no key: err = %v, want errNoCredentials with setup steps", err)
	}

	// A single AuthKey_*.p8 in the config dir: key id from the file name.
	testKey(t, dir, "ABC123XYZ9")
	c, err := loadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if c.KeyID != "ABC123XYZ9" || c.IssuerID != "" {
		t.Errorf("creds = %+v", c)
	}

	// Env overrides config; ASC_PRIVATE_KEY accepts escaped newlines.
	data, _ := os.ReadFile(filepath.Join(dir, "AuthKey_ABC123XYZ9.p8"))
	t.Setenv("ASC_PRIVATE_KEY", strings.ReplaceAll(string(data), "\n", `\n`))
	t.Setenv("ASC_KEY_ID", "ENVKEY")
	t.Setenv("ASC_ISSUER_ID", "issuer-1")
	c, err = loadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if c.KeyID != "ENVKEY" || c.IssuerID != "issuer-1" || c.KeyPath != "" {
		t.Errorf("env creds = %+v", c)
	}
}

func TestLoadCredentialsConfigFileAndKeyDirs(t *testing.T) {
	dir := isolate(t)
	home := os.Getenv("HOME")
	testKey(t, filepath.Join(home, ".appstoreconnect", "private_keys"), "HOMEKEY")
	cfg, _ := json.Marshal(fileConfig{KeyID: "HOMEKEY", IssuerID: "iss", VendorNumber: "8800"})
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "config.json"), cfg, 0o600)
	c, err := loadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(c.KeyPath, filepath.Join(".appstoreconnect", "private_keys", "AuthKey_HOMEKEY.p8")) || c.VendorNumber != "8800" || c.IssuerID != "iss" {
		t.Errorf("creds = %+v", c)
	}
}

func TestParsePrivateKeyRejectsGarbage(t *testing.T) {
	if _, err := parsePrivateKey([]byte("not a key")); err == nil {
		t.Error("want error for non-PEM")
	}
}

func decodeJWT(t *testing.T, tok string, pub *ecdsa.PublicKey) (map[string]any, map[string]any) {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	enc := base64.RawURLEncoding
	var h, c map[string]any
	hb, _ := enc.DecodeString(parts[0])
	cb, _ := enc.DecodeString(parts[1])
	json.Unmarshal(hb, &h)
	json.Unmarshal(cb, &c)
	sig, err := enc.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("signature: %d bytes, %v", len(sig), err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, sum[:], r, s) {
		t.Error("signature does not verify")
	}
	return h, c
}

func TestSignJWT(t *testing.T) {
	dir := t.TempDir()
	k, _ := testKey(t, dir, "K1")
	now := time.Unix(1_800_000_000, 0)

	h, c := decodeJWT(t, mustSign(t, &credentials{KeyID: "K1", IssuerID: "iss-uuid", key: k}, now), &k.PublicKey)
	if h["alg"] != "ES256" || h["kid"] != "K1" || h["typ"] != "JWT" {
		t.Errorf("header = %v", h)
	}
	if c["iss"] != "iss-uuid" || c["aud"] != "appstoreconnect-v1" || c["sub"] != nil {
		t.Errorf("team claims = %v", c)
	}
	if exp, iat := c["exp"].(float64), c["iat"].(float64); exp-iat > 20*60 || iat != float64(now.Unix()) {
		t.Errorf("iat %v exp %v: lifetime must be ≤ 20 min", iat, exp)
	}

	// Individual key: sub "user", no iss.
	_, c = decodeJWT(t, mustSign(t, &credentials{KeyID: "K1", key: k}, now), &k.PublicKey)
	if c["sub"] != "user" || c["iss"] != nil {
		t.Errorf("individual claims = %v", c)
	}
}

func mustSign(t *testing.T, c *credentials, now time.Time) string {
	t.Helper()
	tok, err := signJWT(c, now)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestTokenSourceCaches(t *testing.T) {
	k, _ := testKey(t, t.TempDir(), "K1")
	now := time.Unix(1_800_000_000, 0)
	src := &tokenSource{creds: &credentials{KeyID: "K1", IssuerID: "i", key: k}, now: func() time.Time { return now }}
	a, _ := src.Token()
	now = now.Add(10 * time.Minute)
	if b, _ := src.Token(); b != a {
		t.Error("token re-signed while still valid")
	}
	now = now.Add(9 * time.Minute) // 1 min from expiry
	if b, _ := src.Token(); b == a {
		t.Error("token not refreshed near expiry")
	}
}
