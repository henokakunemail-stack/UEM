package agentupdate

import (
	"fmt"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
)

// manifestForRelease builds the signed statement for a release row.
//
// Every field here is what an agent needs to decide whether to install, and
// each is bound by the signature: url so a manifest cannot be replayed against
// a different artifact, size and sha256 so the bytes the signer certified are
// the bytes the agent downloaded, minimum_supported_version so the downgrade
// floor is the signer's policy rather than the server's.
//
// publishedAt is RFC3339 in UTC because it is signed, and a signed timestamp
// whose formatting is not fixed is a timestamp the two halves of the boundary
// can disagree about while both believing they read it correctly.
func manifestForRelease(rel *AgentRelease, downloadURL string, publishedAt time.Time) protocol.ReleaseManifest {
	return protocol.ReleaseManifest{
		Version:                 rel.Version,
		OSName:                  rel.OSName,
		Arch:                    rel.Arch,
		SHA256Checksum:          rel.SHA256Checksum,
		Size:                    rel.FileSize,
		URL:                     downloadURL,
		PublishedAt:             publishedAt.UTC().Format(time.RFC3339),
		MinimumSupportedVersion: rel.MinimumSupportedVersion,
	}
}

// releaseDownloadURL is the server-relative URL an agent fetches the artifact
// from. It is derived rather than stored so the route and the manifest cannot
// disagree: a row moved to a new storage layout keeps resolving, and the
// signature stays valid because the URL it covers is computed the same way by
// the signer and by the agent.
// publishedAtOf is the timestamp that goes into the signed manifest. A row
// signed by the sign endpoint carries published_at from the signing host;
// backfilled rows and any unsigned one fall back to created_at so the field is
// always present and always a real time rather than a zero value that reads as
// 0001-01-01 on the wire.
func publishedAtOf(rel *AgentRelease) time.Time {
	if rel.PublishedAt != nil {
		return *rel.PublishedAt
	}
	return rel.CreatedAt
}

func releaseDownloadURL(rel *AgentRelease) string {
	if rel.DownloadURL != "" {
		return rel.DownloadURL
	}
	return fmt.Sprintf("/api/agent/releases/%s/download", rel.ID)
}
