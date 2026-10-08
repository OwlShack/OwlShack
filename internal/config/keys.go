package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	meshcore "github.com/OwlShack/meshcore-go"
)

const seedLen = 32 // ed25519 seed bytes

// PubKeyHexFromSeed derives the hex public key from a stored identity hex; "" for empty input.
func PubKeyHexFromSeed(privHex string) (string, error) {
	if strings.TrimSpace(privHex) == "" {
		return "", nil
	}
	li, err := LocalIdentityFromHex(privHex)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(li.PublicKeyBytes()), nil
}

// LocalIdentityFromHex accepts a 32-byte seed (64 hex) or a 64-byte MeshCore expanded private key (128 hex, `get prv.key`).
func LocalIdentityFromHex(privHex string) (meshcore.LocalIdentity, error) {
	b, err := hex.DecodeString(strings.TrimSpace(privHex))
	if err != nil {
		return meshcore.LocalIdentity{}, fmt.Errorf("privateKey must be hex: %w", err)
	}
	switch len(b) {
	case seedLen:
		var s [seedLen]byte
		copy(s[:], b)
		return meshcore.NewLocalIdentityFromSeed(s), nil
	case ed25519.PrivateKeySize:
		return meshcore.NewLocalIdentityFromExpandedKey(b)
	default:
		return meshcore.LocalIdentity{}, fmt.Errorf("privateKey must be %d hex (seed) or %d hex (private key), got %d bytes", seedLen*2, ed25519.PrivateKeySize*2, len(b))
	}
}

// GenerateSeedHex returns a fresh random hex ed25519 seed (a new identity).
func GenerateSeedHex() (string, error) {
	seed := make([]byte, seedLen)
	if _, err := rand.Read(seed); err != nil {
		return "", fmt.Errorf("generating identity seed: %w", err)
	}
	return hex.EncodeToString(seed), nil
}

// EnsureNodeKeys generates a private key for every companion and repeater that has none.
func (c *Config) EnsureNodeKeys() error {
	for i := range c.Companions {
		if c.Companions[i].PrivateKey != "" {
			continue
		}
		seed, err := GenerateSeedHex()
		if err != nil {
			return fmt.Errorf("companion %q: %w", c.Companions[i].Name, err)
		}
		c.Companions[i].PrivateKey = seed
		slog.Info("generated companion identity", "companion", c.Companions[i].Name)
	}
	if c.Repeater != nil && c.Repeater.PrivateKey == "" {
		seed, err := GenerateSeedHex()
		if err != nil {
			return fmt.Errorf("repeater %q: %w", c.Repeater.Name, err)
		}
		c.Repeater.PrivateKey = seed
		slog.Info("generated repeater identity", "repeater", c.Repeater.Name)
	}
	return nil
}

// MigrateKeyFiles inlines legacy keyFile identities into PrivateKey, resolving relative paths against baseDir.
func (c *Config) MigrateKeyFiles(baseDir string) error {
	for i := range c.Companions {
		comp := &c.Companions[i]
		if comp.PrivateKey != "" {
			comp.KeyFile = ""
			continue
		}

		keyFile := strings.TrimSpace(comp.KeyFile)
		if keyFile == "" {
			// Legacy default used by old binaries when keyFile was omitted.
			keyFile = strings.ToValidUTF8(comp.Name, "") + "_companion.key"
		}
		if !filepath.IsAbs(keyFile) {
			keyFile = filepath.Join(baseDir, keyFile)
		}

		data, err := os.ReadFile(keyFile)
		if err != nil {
			if os.IsNotExist(err) {
				slog.Warn("companion key file not found — a new identity will be generated",
					"companion", comp.Name, "keyFile", keyFile)
				comp.KeyFile = ""
				continue
			}
			return fmt.Errorf("companion %q: reading key file %s: %w", comp.Name, keyFile, err)
		}

		seedHex := strings.TrimSpace(string(data))
		if err := validateSeedHex(seedHex); err != nil {
			return fmt.Errorf("companion %q: key file %s: %w", comp.Name, keyFile, err)
		}
		comp.PrivateKey = seedHex
		comp.KeyFile = ""
		slog.Info("inlined companion key file into config", "companion", comp.Name, "keyFile", keyFile)
	}
	return nil
}

func validateSeedHex(s string) error {
	_, err := LocalIdentityFromHex(s)
	return err
}
