package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/notify"
)

func runKeys(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("keys: expected a subcommand (gen|gen-vapid|add|list|rm)")
	}
	switch args[0] {
	case "gen":
		return keysGen(args[1:])
	case "gen-vapid":
		return keysGenVAPID(args[1:])
	case "add":
		return keysAdd(args[1:])
	case "list":
		return keysList(args[1:])
	case "rm":
		return keysRm(args[1:])
	default:
		return fmt.Errorf("keys: unknown subcommand %q", args[0])
	}
}

// keysGen creates a publisher signing keypair and writes <out>.pub (a minisign
// public-key line, ready for `keys add`) and <out>.key (the raw ed25519 private
// key, base64, mode 0600) for signing. `--out ~/.bnbloc/component-publisher`
// writes the key the signing commands use by default. An existing key is
// never overwritten unless --force is given.
func keysGen(args []string) error {
	fs := flag.NewFlagSet("keys gen", flag.ContinueOnError)
	out := fs.String("out", "bnbloc-publisher", "output filename prefix (e.g. ~/.bnbloc/component-publisher)")
	force := fs.Bool("force", false, "overwrite an existing keypair")
	if err := fs.Parse(args); err != nil {
		return err
	}
	prefix := expandHome(*out)
	if !*force {
		for _, path := range []string{prefix + ".key", prefix + ".pub"} {
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("keys gen: %s already exists (use --force to replace the keypair)", path)
			}
		}
	}
	pubMinisign, keyID, priv, err := pkgmeta.GenerateSigningKey()
	if err != nil {
		return err
	}
	// The private key is written first and atomically with mode 0600, so it
	// is never readable by others, even when replacing an older file.
	if err := writeFileAtomic(prefix+".key", []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600); err != nil {
		return err
	}
	if err := writeFileAtomic(prefix+".pub", []byte(pubMinisign+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s.pub (keyId %s) and %s.key (0600)\n", prefix, keyID, prefix)
	return nil
}

// keysGenVAPID prints a new VAPID key pair for the browser push channel as
// the realtime environment lines that enable it. Nothing is written to disk:
// the private key belongs in the deployment's secret store.
func keysGenVAPID(args []string) error {
	fs := flag.NewFlagSet("keys gen-vapid", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	pub, priv, err := notify.GenerateVAPIDKeys()
	if err != nil {
		return err
	}
	fmt.Printf("%s=%s\n%s=%s\n", notify.WebPushPublicKeyEnv, pub, notify.WebPushPrivateKeyEnv, priv)
	fmt.Fprintf(os.Stderr, "set both on bnbloc-realtime (plus %s=mailto:<ops address> unless %s is https); keep the private key secret\n",
		notify.WebPushSubjectEnv, notify.PublicURLEnv)
	return nil
}

func keysAdd(args []string) error {
	fs := flag.NewFlagSet("keys add", flag.ContinueOnError)
	gateway := fs.String("gateway", "", "")
	token := fs.String("token", "", "")
	pubFile := fs.String("pubkey", "", "minisign public key file")
	label := fs.String("label", "", "optional label")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pubFile == "" {
		return fmt.Errorf("keys add: --pubkey required")
	}
	raw, err := os.ReadFile(expandHome(*pubFile))
	if err != nil {
		return err
	}
	c := newClient(resolveConfig(*gateway, *token))
	req, _ := json.Marshal(map[string]string{"publicKey": strings.TrimSpace(string(raw)), "label": *label})
	if _, err := c.do("POST", "/v1/platform/component-keys", req); err != nil {
		return err
	}
	fmt.Println("added")
	return nil
}

func keysList(args []string) error {
	fs := flag.NewFlagSet("keys list", flag.ContinueOnError)
	gateway := fs.String("gateway", "", "")
	token := fs.String("token", "", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c := newClient(resolveConfig(*gateway, *token))
	body, err := c.do("GET", "/v1/platform/component-keys", nil)
	if err != nil {
		return err
	}
	fmt.Println(string(body))
	return nil
}

func keysRm(args []string) error {
	fs := flag.NewFlagSet("keys rm", flag.ContinueOnError)
	gateway := fs.String("gateway", "", "")
	token := fs.String("token", "", "")
	keyID, err := parseNamed(fs, args)
	if err != nil {
		return err
	}
	if keyID == "" {
		return fmt.Errorf("usage: keys rm <keyId>")
	}
	c := newClient(resolveConfig(*gateway, *token))
	if _, err := c.do("DELETE", "/v1/platform/component-keys/"+url.PathEscape(keyID), nil); err != nil {
		return err
	}
	fmt.Println("removed")
	return nil
}

func defaultDaemonClient() *http.Client { return &http.Client{Timeout: 10 * time.Second} }

// loadPrivateKey reads an ed25519 private key from a file. The content may be
// base64 or hex of the raw 64-byte key (as written by `keys gen`).
func loadPrivateKey(file string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(string(raw))
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(b), nil
	}
	if b, err := hex.DecodeString(s); err == nil && len(b) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(b), nil
	}
	return nil, fmt.Errorf("private key must be base64 or hex of a %d-byte ed25519 key", ed25519.PrivateKeySize)
}
