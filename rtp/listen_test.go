// Copyright 2026 LiveKit, Inc.
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

package rtp

import (
	"net"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListenUDPEvenPortRange(t *testing.T) {
	ip := netip.AddrFrom4([4]byte{127, 0, 0, 1})

	// block the first even port; allocation must skip to the next one
	const portMin, portMax = 34200, 34203
	blocker, err := net.ListenUDP("udp", &net.UDPAddr{IP: ip.AsSlice(), Port: 34200})
	require.NoError(t, err)
	defer blocker.Close()

	c, err := ListenUDPEvenPortRange(portMin, portMax, ip)
	require.NoError(t, err)
	defer c.Close()
	require.Equal(t, 34202, c.LocalAddr().(*net.UDPAddr).Port)

	// even ports exhausted
	_, err = ListenUDPEvenPortRange(portMin, portMax, ip)
	require.ErrorIs(t, err, ErrListenFailed)
}
