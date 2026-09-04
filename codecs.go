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
	"fmt"
	"slices"
	"strings"
)

type Kind int

const (
	Unknown = Kind(iota)
	Audio
	Data
)

type CodecParams []CodecParam

func (arr CodecParams) String() string {
	params := make([]string, 0, len(arr))
	for _, p := range arr {
		params = append(params, p.String())
	}
	return strings.Join(params, ";")
}

func (arr CodecParams) Get(key string) (string, bool) {
	for _, p := range arr {
		if p.Key == key {
			return p.Val, true
		}
	}
	return "", false
}
func (arr CodecParams) Has(key string) bool {
	for _, p := range arr {
		if p.Key == key {
			return true
		}
	}
	return false
}
func (arr CodecParams) HasValue(key, val string) bool {
	for _, p := range arr {
		if p.Key == key {
			return p.Val == val
		}
	}
	return false
}
func (arr CodecParams) HasParam(p2 CodecParam) bool {
	for _, p := range arr {
		if p.Key == p2.Key {
			return p.Val == p2.Val
		}
	}
	return false
}

func (arr *CodecParams) Add(key, val string) {
	*arr = append(*arr, CodecParam{Key: key, Val: val})
}

func (arr CodecParams) Equals(arr2 CodecParams) bool {
	if len(arr) != len(arr2) {
		return false
	}
	for i := range arr {
		p1, p2 := arr[i], arr2[i]
		if p1 != p2 {
			return false
		}
	}
	return true
}

type CodecParam struct {
	Key string
	Val string
}

func (p CodecParam) String() string {
	if p.Val == "" {
		return p.Key
	}
	return p.Key + "=" + p.Val
}

type CodecConfig struct {
	SampleRate int         // codec sample rate (opus/<rate>)
	Channels   int         // number of channels if specified (opus/48000/<channels>)
	Params     CodecParams // a list of codec params (fmtp)
}

func (c *CodecConfig) Equals(c2 *CodecConfig) bool {
	if c == nil && c2 == nil {
		return true
	} else if c == nil || c2 == nil {
		return false
	}
	return c.SampleRate == c2.SampleRate &&
		c.Channels == c2.Channels &&
		c.Params.Equals(c2.Params)
}

type CodecTypeInfo struct {
	Name        string // codec name for SDP, must not contain '/' parameters
	Kind        Kind
	RTPDefType  byte
	RTPIsStatic bool
	Priority    int // higher is preferable
	FileExt     string
}

func (c CodecTypeInfo) String() string {
	return c.SDPName()
}
func (c CodecTypeInfo) SDPName() string {
	return c.Name
}
func (c CodecTypeInfo) Info() CodecTypeInfo {
	return c
}

type CreateFunc func() Codec
type OfferFunc func(s *CodecSet) []CodecInfo
type SupportsFunc func(c CodecConfig) (CodecInfo, CreateFunc, bool)

type CodecType interface {
	// SDPName is a name of the codec in SDP. Must not contain '/' parameters.
	SDPName() string
	// Info returns static information about this codec.
	Info() CodecTypeInfo
	// Offer lists the default set of codec configurations for SDP offers.
	Offer(s *CodecSet) []CodecInfo
	// Supports checks if a given codec configuration is supported.
	// It returns full codec information for it with accepted configuration, and a constructor for creating the codec.
	Supports(c CodecConfig) (CodecInfo, CreateFunc, bool)
}

type CodecInfo struct {
	CodecTypeInfo
	CodecConfig
	RTPClockRate int
}

func (c CodecInfo) String() string {
	return c.SDPFullName()
}

func (c CodecInfo) SDPFullName() string {
	if c.Channels == 0 {
		return fmt.Sprintf("%s/%d", c.SDPName(), c.RTPClockRate)
	}
	return fmt.Sprintf("%s/%d/%d", c.SDPName(), c.RTPClockRate, c.Channels)
}

func (c CodecInfo) Info() CodecInfo {
	c.Defaults()
	return c
}

func (c *CodecInfo) Defaults() {
	if c.RTPClockRate == 0 {
		c.RTPClockRate = c.SampleRate
	}
}

func (c *CodecInfo) Equals(c2 *CodecInfo) bool {
	if c == nil && c2 == nil {
		return true
	} else if c == nil || c2 == nil {
		return false
	}
	return c.Name == c2.Name && c.CodecConfig.Equals(&c2.CodecConfig)
}

// Codec is a configured instance of a CodecType.
type Codec interface {
	Info() CodecInfo
}

var (
	globalSet       = NewCodecSet()
	codecs          []CodecType
	codecOnRegister []func(c CodecType)
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
	if i := strings.IndexByte(name, '/'); i >= 0 {
		name = name[:i]
	}
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
	if i := strings.IndexByte(name, '/'); i >= 0 {
		name = name[:i]
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
func (s *CodecSet) IsEnabled(c CodecType) bool {
	if s == nil || c == nil {
		return false
	}
	return s.IsEnabledByName(c.SDPName())
}

// ListEnabled lists all enabled codecs.
func (s *CodecSet) ListEnabled() []CodecType {
	if s == nil {
		return nil
	}
	out := make([]CodecType, 0, len(codecs))
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
func CodecEnabled(c CodecType) bool {
	return GlobalCodecs().IsEnabled(c)
}

// CodecEnabledByName checks if the codec name is enabled in the GlobalCodecs set.
func CodecEnabledByName(name string) bool {
	return GlobalCodecs().IsEnabledByName(name)
}

func OnRegister(fnc func(c CodecType)) {
	// Call it on already registered codecs first, so that the import order doesn't matter.
	for _, c := range codecs {
		fnc(c)
	}
	// Add the function for codecs that will be registered next.
	codecOnRegister = append(codecOnRegister, fnc)
}

// Codecs lists all registered codecs.
func Codecs() []CodecType {
	return slices.Clone(codecs)
}

// EnabledCodecs lists all codecs enabled in the GlobalCodecs set.
func EnabledCodecs() []CodecType {
	return GlobalCodecs().ListEnabled()
}

// RegisterCodec registers the codec.
func RegisterCodec(c CodecType) {
	global := GlobalCodecs()
	codecs = append(codecs, c)
	global.SetEnabled(c.SDPName(), true)
	for _, fnc := range codecOnRegister {
		fnc(c)
	}
}

// NewCodec creates a generic codec definition without a specific implementation.
func NewCodec(info CodecTypeInfo, offer OfferFunc, support SupportsFunc) CodecType {
	if info.Name == "" {
		panic("codec name must be specified")
	}
	if strings.ContainsAny(info.Name, " /") {
		panic("invalid codec name: must not contain '/' or spaces")
	}
	if info.Kind == Unknown {
		panic("codec kind must be specified")
	}
	if offer == nil {
		// Use default config that SupportsFunc generates.
		offer = func(c *CodecSet) []CodecInfo {
			info, _, ok := support(CodecConfig{})
			if !ok {
				return nil // no default
			}
			info.Defaults()
			return []CodecInfo{info}
		}
	}
	return &baseCodecType{CodecTypeInfo: info, offer: offer, support: support}
}

type baseCodecType struct {
	CodecTypeInfo
	offer   OfferFunc
	support SupportsFunc
}

func (t *baseCodecType) Offer(s *CodecSet) []CodecInfo {
	if !s.IsEnabled(t) {
		return nil
	}
	out := t.offer(s)
	for i := range out {
		out[i].Defaults()
	}
	return out
}

func (t *baseCodecType) Supports(c CodecConfig) (CodecInfo, CreateFunc, bool) {
	info, create, ok := t.support(c)
	if ok {
		info.Defaults()
	}
	return info, create, ok
}
