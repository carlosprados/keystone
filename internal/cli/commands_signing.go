package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/carlosprados/keystone/internal/manifest"
	"github.com/carlosprados/keystone/internal/security"
	"github.com/carlosprados/keystone/internal/signing"
	"github.com/spf13/cobra"
)

func signingCommands() []*cobra.Command {
	return []*cobra.Command{
		signCommand(),
		verifySigCommand(),
		manifestCommand(),
	}
}

// signerFlags are shared by every command that produces a signature.
func signerFlags(cmd *cobra.Command, key, cert *string) {
	cmd.Flags().StringVar(key, "key", "", "PEM private key to sign with (PKCS#8, PKCS#1 or SEC1)")
	cmd.Flags().StringVar(cert, "cert", "", "PEM certificate for the key; when given, the signature is checked against it before being written")
	_ = cmd.MarkFlagRequired("key")
}

func signCommand() *cobra.Command {
	var keyPath, certPath, outPath string

	cmd := &cobra.Command{
		Use:     "sign <file>",
		Short:   "Sign a file, producing a detached <file>.sig",
		GroupID: groupLocal,
		Args:    cobra.ExactArgs(1),
		Long: `Produce the detached signature Keystone verifies: over the file's SHA-256
digest, chaining to your trust bundle through the certificate you publish
alongside it. RSA, ECDSA and Ed25519 keys all work.

Purely local. No agent is contacted, and the agent binary cannot do this: it
verifies signatures and never makes them, so a compromised gateway cannot forge
an update for the rest of the fleet.

Sign recipes, artifacts and dataset manifests with it. When --cert is given the
signature is verified against that certificate before anything is written, so a
mismatched key and certificate fail here rather than on every device.`,
		Example: `  keystonectl sign com.example.api.recipe.toml
  keystonectl sign --key signer.key --cert signer.pem dist/api.tar.gz
  keystonectl sign --key signer.key -o /tmp/api.sig dist/api.tar.gz`,
		RunE: runs(func(_ *cobra.Command, args []string) error {
			target := args[0]
			sig, err := signing.SignFile(signing.FileBackend{KeyPath: keyPath, CertPath: certPath}, target)
			if err != nil {
				return err
			}
			out := outPath
			if out == "" {
				out = target + ".sig"
			}
			if err := signing.WriteSignature(out, sig); err != nil {
				return err
			}
			fmt.Printf("signed %s -> %s (%d bytes)\n", target, out, len(sig))
			return nil
		}),
	}
	signerFlags(cmd, &keyPath, &certPath)
	cmd.Flags().StringVarP(&outPath, "out", "o", "", "Where to write the signature (default: <file>.sig)")
	return cmd
}

func verifySigCommand() *cobra.Command {
	var sigPath, certPath, bundlePath string

	cmd := &cobra.Command{
		Use:     "verify <file>",
		Short:   "Verify a detached signature the way the agent will",
		GroupID: groupLocal,
		Args:    cobra.ExactArgs(1),
		Long: `Check a signature exactly as the agent checks it: the certificate must chain
to the trust bundle, and the signature must be over the file's SHA-256 digest.

This is the command to run in CI before publishing. A signature that fails here
fails on every device in the fleet, and finding that out now costs nothing.

Defaults follow the same conventions as the agent: <file>.sig for the signature,
<file>.crt or KEYSTONE_LEAF_CERT for the certificate, and KEYSTONE_TRUST_BUNDLE
for the roots.`,
		Example: `  keystonectl verify com.example.api.recipe.toml
  keystonectl verify --trust-bundle ca.pem --cert signer.pem dist/api.tar.gz`,
		RunE: runs(func(_ *cobra.Command, args []string) error {
			target := args[0]
			sig := sigPath
			if sig == "" {
				sig = target + ".sig"
			}
			cert := certPath
			if cert == "" {
				cert = defaultCertFor(target)
			}
			if cert == "" {
				return fmt.Errorf("no certificate: pass --cert, put one at %s.crt, or set KEYSTONE_LEAF_CERT", target)
			}
			bundle := bundlePath
			if bundle == "" {
				bundle = os.Getenv("KEYSTONE_TRUST_BUNDLE")
			}
			if bundle == "" {
				return fmt.Errorf("no trust bundle: pass --trust-bundle or set KEYSTONE_TRUST_BUNDLE")
			}
			roots, err := security.LoadTrustBundle(bundle)
			if err != nil {
				return err
			}
			if err := security.VerifyDetached(target, sig, cert, roots); err != nil {
				return err
			}
			fmt.Printf("OK: %s verifies against %s\n", target, bundle)
			return nil
		}),
	}
	cmd.Flags().StringVar(&sigPath, "sig", "", "Signature file (default: <file>.sig)")
	cmd.Flags().StringVar(&certPath, "cert", "", "Signing certificate (default: <file>.crt, or KEYSTONE_LEAF_CERT)")
	cmd.Flags().StringVar(&bundlePath, "trust-bundle", "", "CA bundle to chain to (default: KEYSTONE_TRUST_BUNDLE)")
	return cmd
}

// defaultCertFor mirrors how the agent looks for a leaf certificate.
func defaultCertFor(target string) string {
	sibling := target + ".crt"
	if _, err := os.Stat(sibling); err == nil {
		return sibling
	}
	return os.Getenv("KEYSTONE_LEAF_CERT")
}

func manifestCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "manifest",
		Short:   "Create, sign and check dataset manifests",
		GroupID: groupLocal,
		Args:    cobra.NoArgs,
		Long: `A dataset manifest is the signed document that says which version of a
dataset is current. It exists because a dataset's digest cannot live in a recipe:
a feed published every night would need a newly signed recipe every night, and
reconcile would answer a changed recipe by restarting the component.

The manifest carries the parts that change daily — version, digest, publication
time — and its own signature.

Purely local: no agent is contacted by any of these subcommands.`,
		Example: `  keystonectl manifest new --name com.example.cve-bundle --uri https://... bundle.tar
  keystonectl manifest sign --key signer.key --cert signer.pem com.example.cve-bundle.manifest.toml
  keystonectl manifest verify --trust-bundle ca.pem com.example.cve-bundle.manifest.toml`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(manifestNewCommand(), manifestSignCommand(), manifestVerifyCommand())
	return cmd
}

func manifestNewCommand() *cobra.Command {
	var name, version, uri, published, outPath string
	var deltaServer string

	cmd := &cobra.Command{
		Use:   "new <artifact-file>",
		Short: "Write a manifest for a dataset file",
		Args:  cobra.ExactArgs(1),
		Long: `Build a manifest from the dataset file itself: it hashes the file and records
its size, so the digest cannot disagree with the bytes you publish.

--published is the anti-replay anchor and defaults to now. Agents refuse any
manifest not strictly newer than the last one they accepted, which is what stops
an attacker who can serve your URL from replaying a valid, signed bundle from six
months ago. Keep it monotonic across publications.`,
		Example: `  keystonectl manifest new --name com.example.cve-bundle \
      --version 2026-08-15 \
      --uri https://hub.plant.local/datasets/cve-2026-08-15.tar \
      cve-2026-08-15.tar`,
		RunE: runs(func(_ *cobra.Command, args []string) error {
			target := args[0]
			st, err := os.Stat(target)
			if err != nil {
				return err
			}
			digest, err := signing.DigestFile(target)
			if err != nil {
				return err
			}
			ts := time.Now().UTC()
			if published != "" {
				ts, err = time.Parse(time.RFC3339, published)
				if err != nil {
					return fmt.Errorf("--published must be RFC3339 (like 2026-08-15T03:00:00Z): %w", err)
				}
			}
			if version == "" {
				version = ts.Format("2006-01-02")
			}

			m := &manifest.Manifest{
				Schema:    manifest.SchemaVersion,
				Name:      name,
				Version:   version,
				Published: ts,
				Artifact: manifest.Artifact{
					URI:    uri,
					SHA256: fmt.Sprintf("%x", digest),
					Size:   st.Size(),
				},
			}
			if deltaServer != "" {
				m.Delta = &manifest.Delta{Server: deltaServer}
			}
			if err := m.Validate(); err != nil {
				return err
			}

			out := outPath
			if out == "" {
				out = name + ".manifest.toml"
			}
			if err := os.WriteFile(out, renderManifest(m), 0o644); err != nil {
				return err
			}
			fmt.Printf("wrote %s\nnow sign it: keystonectl manifest sign --key <key> %s\n", out, out)
			return nil
		}),
	}
	cmd.Flags().StringVar(&name, "name", "", "Dataset name, as the recipe refers to it")
	cmd.Flags().StringVar(&version, "version", "", "Human-readable version label (default: the published date)")
	cmd.Flags().StringVar(&uri, "uri", "", "Where agents download the dataset from")
	cmd.Flags().StringVar(&published, "published", "", "RFC3339 publication time (default: now). Must increase between publications")
	cmd.Flags().StringVar(&deltaServer, "delta-server", "", "Base URL of a delta server, when patches are published")
	cmd.Flags().StringVarP(&outPath, "out", "o", "", "Where to write the manifest (default: <name>.manifest.toml)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("uri")
	return cmd
}

// renderManifest writes the document by hand rather than through an encoder, so
// the published file keeps a stable field order and its comments.
func renderManifest(m *manifest.Manifest) []byte {
	out := fmt.Sprintf(`schema    = %d
name      = %q
version   = %q
published = %s

[artifact]
uri    = %q
sha256 = %q
size   = %d
`,
		m.Schema, m.Name, m.Version, m.Published.Format(time.RFC3339),
		m.Artifact.URI, m.Artifact.SHA256, m.Artifact.Size)

	if m.Delta != nil {
		out += fmt.Sprintf("\n[delta]\nserver = %q\n", m.Delta.Server)
	}
	return []byte(out)
}

func manifestSignCommand() *cobra.Command {
	var keyPath, certPath string

	cmd := &cobra.Command{
		Use:   "sign <manifest.toml>",
		Short: "Sign a manifest, producing <manifest.toml>.sig",
		Args:  cobra.ExactArgs(1),
		Long: `Validate the manifest and sign it. The manifest is parsed first, so a broken
document is caught before it gets a signature that makes it look trustworthy.`,
		Example: `  keystonectl manifest sign --key signer.key --cert signer.pem cve.manifest.toml`,
		RunE: runs(func(_ *cobra.Command, args []string) error {
			target := args[0]
			m, err := manifest.Load(target)
			if err != nil {
				return err
			}
			reportUnknownManifestFields(m)
			sig, err := signing.SignFile(signing.FileBackend{KeyPath: keyPath, CertPath: certPath}, target)
			if err != nil {
				return err
			}
			out := manifest.SigPath(target)
			if err := signing.WriteSignature(out, sig); err != nil {
				return err
			}
			fmt.Printf("signed %s (%s, published %s) -> %s\n",
				m.Name, m.Version, m.Published.Format(time.RFC3339), out)
			return nil
		}),
	}
	signerFlags(cmd, &keyPath, &certPath)
	return cmd
}

func manifestVerifyCommand() *cobra.Command {
	var certPath, bundlePath, since string

	cmd := &cobra.Command{
		Use:   "verify <manifest.toml>",
		Short: "Check a manifest the way an agent will",
		Args:  cobra.ExactArgs(1),
		Long: `Validate the document, verify its signature against the trust bundle, and
optionally check the anti-replay rule with --since.

Run it in CI on every publication. --since takes the publication time of the
manifest currently in the field: agents refuse anything not strictly newer, so a
manifest that fails that check is one no device would accept.`,
		Example: `  keystonectl manifest verify --trust-bundle ca.pem cve.manifest.toml
  keystonectl manifest verify --since 2026-08-14T03:00:00Z cve.manifest.toml`,
		RunE: runs(func(_ *cobra.Command, args []string) error {
			target := args[0]
			m, err := manifest.Load(target)
			if err != nil {
				return err
			}
			reportUnknownManifestFields(m)

			cert := certPath
			if cert == "" {
				cert = defaultCertFor(target)
			}
			bundle := bundlePath
			if bundle == "" {
				bundle = os.Getenv("KEYSTONE_TRUST_BUNDLE")
			}
			if cert != "" && bundle != "" {
				roots, err := security.LoadTrustBundle(bundle)
				if err != nil {
					return err
				}
				if err := security.VerifyDetached(target, manifest.SigPath(target), cert, roots); err != nil {
					return err
				}
				fmt.Printf("signature OK against %s\n", bundle)
			} else {
				fmt.Printf("document OK; signature NOT checked (no %s)\n",
					pick(cert == "", "certificate", "trust bundle"))
			}

			if since != "" {
				last, err := time.Parse(time.RFC3339, since)
				if err != nil {
					return fmt.Errorf("--since must be RFC3339: %w", err)
				}
				if !m.IsNewerThan(last) {
					return fmt.Errorf("published %s is not newer than %s: no agent would accept this manifest",
						m.Published.Format(time.RFC3339), since)
				}
				fmt.Printf("newer than %s: agents will accept it\n", since)
			}

			fmt.Printf("%s %s, published %s, %d bytes\n",
				m.Name, m.Version, m.Published.Format(time.RFC3339), m.Artifact.Size)
			return nil
		}),
	}
	cmd.Flags().StringVar(&certPath, "cert", "", "Signing certificate (default: <manifest>.crt, or KEYSTONE_LEAF_CERT)")
	cmd.Flags().StringVar(&bundlePath, "trust-bundle", "", "CA bundle to chain to (default: KEYSTONE_TRUST_BUNDLE)")
	cmd.Flags().StringVar(&since, "since", "", "Publication time of the manifest currently deployed; fails if this one is not newer")
	return cmd
}

func reportUnknownManifestFields(m *manifest.Manifest) {
	for _, u := range m.UnknownFields {
		fmt.Fprintf(os.Stderr, "warning: %s is not a field this build understands: %s\n", filepath.Base(m.Name), u)
	}
}

func pick(cond bool, whenTrue, whenFalse string) string {
	if cond {
		return whenTrue
	}
	return whenFalse
}
