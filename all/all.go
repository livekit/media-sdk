package all

// Register all supported pure-Go codecs. CGo codecs must be in all_cgo.go.
import (
	_ "github.com/livekit/media-sdk/dtmf"
	_ "github.com/livekit/media-sdk/g711"
	_ "github.com/livekit/media-sdk/g722"
)
