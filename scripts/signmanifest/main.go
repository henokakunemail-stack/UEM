package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
)

func main() {
	var (
		privKeyB64 string
		relJSON    string
		relFile    string
		minVer     string
		serverURL  string
		token      string
	)

	flag.StringVar(&privKeyB64, "private-key", "", "Base64 encoded 64-byte Ed25519 private key")
	flag.StringVar(&relJSON, "release-json", "", "JSON body of GET /api/agent-updates/releases/{id}")
	flag.StringVar(&relFile, "release-file", "", "Path to file containing release JSON")
	flag.StringVar(&minVer, "min-version", "", "Minimum supported version floor")
	flag.StringVar(&serverURL, "server-url", "", "Server base URL (e.g. https://uem.example.com)")
	flag.StringVar(&token, "token", "", "Bearer token for server API auth")
	flag.Parse()

	if privKeyB64 == "" {
		fmt.Fprintln(os.Stderr, "error: -private-key is required")
		os.Exit(1)
	}

	privKeyBytes, err := base64.StdEncoding.DecodeString(privKeyB64)
	if err != nil || len(privKeyBytes) != ed25519.PrivateKeySize {
		fmt.Fprintf(os.Stderr, "error: -private-key must be valid base64 64-byte Ed25519 key (got %d bytes)\n", len(privKeyBytes))
		os.Exit(1)
	}
	privKey := ed25519.PrivateKey(privKeyBytes)

	var jsonBytes []byte
	if relJSON != "" {
		jsonBytes = []byte(relJSON)
	} else if relFile != "" {
		b, err := os.ReadFile(relFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: read release-file: %v\n", err)
			os.Exit(1)
		}
		jsonBytes = b
	} else {
		fmt.Fprintln(os.Stderr, "error: either -release-json or -release-file is required")
		os.Exit(1)
	}

	var rel struct {
		ID             string `json:"id"`
		Version        string `json:"version"`
		OSName         string `json:"os_name"`
		Arch           string `json:"arch"`
		FileSize       int64  `json:"file_size"`
		SHA256Checksum string `json:"sha256_checksum"`
		DownloadURL    string `json:"download_url"`
		CreatedAt      string `json:"created_at"`
	}

	if err := json.Unmarshal(jsonBytes, &rel); err != nil {
		fmt.Fprintf(os.Stderr, "error: parse release JSON: %v\n", err)
		os.Exit(1)
	}

	downloadURL := rel.DownloadURL
	if downloadURL == "" {
		downloadURL = fmt.Sprintf("/api/agent/releases/%s/download", rel.ID)
	}

	publishedAt := time.Now().UTC()
	manifest := protocol.ReleaseManifest{
		Version:                 rel.Version,
		OSName:                  rel.OSName,
		Arch:                    rel.Arch,
		SHA256Checksum:          rel.SHA256Checksum,
		Size:                    rel.FileSize,
		URL:                     downloadURL,
		PublishedAt:             publishedAt.Format(time.RFC3339),
		MinimumSupportedVersion: minVer,
	}

	sig, err := manifest.Sign(privKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: sign manifest: %v\n", err)
		os.Exit(1)
	}

	out := map[string]string{
		"release_id":                rel.ID,
		"signature":                 sig,
		"minimum_supported_version": minVer,
		"download_url":              downloadURL,
		"published_at":              publishedAt.Format(time.RFC3339),
	}

	if serverURL != "" && token != "" {
		body, _ := json.Marshal(out)
		url := fmt.Sprintf("%s/api/agent-updates/releases/%s/sign", serverURL, rel.ID)
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: create sign request: %v\n", err)
			os.Exit(1)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: send sign request: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		respBytes, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			fmt.Fprintf(os.Stderr, "error: sign endpoint returned %d: %s\n", resp.StatusCode, string(respBytes))
			os.Exit(1)
		}
		fmt.Println("Release successfully signed and submitted to server:", string(respBytes))
	} else {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	}
}
