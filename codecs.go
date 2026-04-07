// Copyright 2024 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// 	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package media

import (
	"slices"
	"strings"
)

type CodecInfo struct {
	SDPName      string
	SampleRate   int
	RTPClockRate int
	RTPDefType   byte
	RTPIsStatic  bool
	Priority     int
	Disabled     bool // codec is disabled in GlobalCodecs by default
	Hidden       bool // codec should not appear in SDP offer, but can be used in the answer
	FileExt      string
}

type Codec interface {
	Info() CodecInfo
}

var (
	globalSet       = NewCodecSet()
	codecs          []Codec
	codecOnRegister []func(c Codec)
)

// GlobalCodecs returns a shared codec set.
func GlobalCodecs() *CodecSet {
	return globalSet
}

// NewCodecSet creates an empty codec set. All codecs are disabled, unless enabled explicitly.
func NewCodecSet() *CodecSet {
	return &CodecSet{
		enabled: make(map[string]bool),
	}
}

// CodecSet represents a set of codecs that can be enabled or disabled.
type CodecSet struct {
	parent  *CodecSet
	enabled map[string]bool
}

// NewSet creates a codec set that overlays the current codec set.
// It will inherit all codecs enabled in the parent set.
func (s *CodecSet) NewSet() *CodecSet {
	c2 := NewCodecSet()
	c2.parent = s
	return c2
}

// SetEnabled enables or disables a given codec.
func (s *CodecSet) SetEnabled(name string, enabled bool) {
	name = strings.ToLower(name)
	s.enabled[name] = enabled
}

// SetEnabledMap is the same as SetEnabled, but accepts a map with multiple codecs.
func (s *CodecSet) SetEnabledMap(codecs map[string]bool) {
	for name, enabled := range codecs {
		s.SetEnabled(name, enabled)
	}
}

// IsEnabledByName checks if a given codec is enabled by its name.
func (s *CodecSet) IsEnabledByName(name string) bool {
	if s == nil {
		return false
	}
	name = strings.ToLower(name)
	for s := s; s != nil; s = s.parent {
		if enabled, ok := s.enabled[name]; ok {
			return enabled
		}
	}
	return false
}

// IsEnabled checks if a given codec is enabled.
func (s *CodecSet) IsEnabled(c Codec) bool {
	if s == nil || c == nil {
		return false
	}
	return s.IsEnabledByName(c.Info().SDPName)
}

// ListEnabled lists all enabled codecs.
func (s *CodecSet) ListEnabled() []Codec {
	if s == nil {
		return nil
	}
	out := make([]Codec, 0, len(codecs))
	for _, c := range codecs {
		if s.IsEnabled(c) {
			out = append(out, c)
		}
	}
	return out
}

// CodecSetEnabled enables or disables a codec in the GlobalCodecs set.
func CodecSetEnabled(name string, enabled bool) {
	GlobalCodecs().SetEnabled(name, enabled)
}

// CodecsSetEnabled enables or disables multiple codecs in the GlobalCodecs set.
func CodecsSetEnabled(codecs map[string]bool) {
	GlobalCodecs().SetEnabledMap(codecs)
}

// CodecEnabled checks if the codec is enabled in the GlobalCodecs set.
func CodecEnabled(c Codec) bool {
	return GlobalCodecs().IsEnabled(c)
}

// CodecEnabledByName checks if the codec name is enabled in the GlobalCodecs set.
func CodecEnabledByName(name string) bool {
	return GlobalCodecs().IsEnabledByName(name)
}

func OnRegister(fnc func(c Codec)) {
	// Call it on already registered codecs first, so that the import order doesn't matter.
	for _, c := range codecs {
		fnc(c)
	}
	// Add the function for codecs that will be registered next.
	codecOnRegister = append(codecOnRegister, fnc)
}

// Codecs lists all registered codecs.
func Codecs() []Codec {
	return slices.Clone(codecs)
}

// EnabledCodecs lists all codecs enabled in the GlobalCodecs set.
func EnabledCodecs() []Codec {
	return GlobalCodecs().ListEnabled()
}

// RegisterCodec registers the codec.
func RegisterCodec(c Codec) {
	info := c.Info()
	global := GlobalCodecs()
	codecs = append(codecs, c)
	global.SetEnabled(info.SDPName, !info.Disabled)
	for _, fnc := range codecOnRegister {
		fnc(c)
	}
}

// NewCodec creates a generic codec definition without a specific implementation.
func NewCodec(info CodecInfo) Codec {
	if info.SampleRate <= 0 {
		panic("invalid sample rate")
	}
	if info.RTPClockRate == 0 {
		info.RTPClockRate = info.SampleRate
	}
	return &baseCodec{info: info}
}

type baseCodec struct {
	info CodecInfo
}

func (c *baseCodec) Info() CodecInfo {
	return c.info
}
