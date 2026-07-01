package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

func main() {
	archivePath := flag.String("archive", "", "Docker image archive to push")
	refValue := flag.String("ref", "", "registry reference to publish")
	caPath := flag.String("ca", "", "PEM CA bundle trusted for the registry")
	timeout := flag.Duration("timeout", 30*time.Second, "push timeout")
	flag.Parse()

	if *archivePath == "" || *refValue == "" || *caPath == "" {
		fmt.Fprintln(os.Stderr, "-archive, -ref, and -ca are required")
		os.Exit(2)
	}

	if err := push(*archivePath, *refValue, *caPath, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "push OCI archive: %v\n", err)
		os.Exit(1)
	}
}

func push(archivePath string, refValue string, caPath string, timeout time.Duration) error {
	ref, err := name.ParseReference(refValue)
	if err != nil {
		return fmt.Errorf("parse reference %q: %w", refValue, err)
	}

	img, err := tarball.ImageFromPath(archivePath, nil)
	if err != nil {
		return fmt.Errorf("read image archive %q: %w", archivePath, err)
	}

	caPEM, err := os.ReadFile(caPath) // #nosec G304 -- E2E helper intentionally reads a caller-supplied CA bundle.
	if err != nil {
		return fmt.Errorf("read CA bundle %q: %w", caPath, err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return fmt.Errorf("load system cert pool: %w", err)
	}
	if roots == nil {
		roots = x509.NewCertPool()
	}
	if ok := roots.AppendCertsFromPEM(caPEM); !ok {
		return fmt.Errorf("CA bundle %q did not contain PEM certificates", caPath)
	}

	defaultTransport, ok := remote.DefaultTransport.(*http.Transport)
	if !ok {
		return fmt.Errorf("unexpected go-containerregistry default transport type %T", remote.DefaultTransport)
	}
	transport := defaultTransport.Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := remote.Write(ref, img, remote.WithContext(ctx), remote.WithTransport(transport)); err != nil {
		return fmt.Errorf("write %s: %w", ref.Name(), err)
	}
	return nil
}
