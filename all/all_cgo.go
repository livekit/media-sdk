//go:build cgo

package all

// Register all supported codecs that use CGo.
import (
	_ "github.com/livekit/media-sdk/opus"
)
