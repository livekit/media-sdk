// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package jitter

import "time"

// A sequence line is a run of packets on one SSRC that continue each other's
// sequence numbering. Normally there is one. Some senders produce two on the
// same SSRC.
//
// prevSN alone cannot tell which line is live. Whichever line moved prevSN last
// wins, so a sparse line ahead of the live one silences it, and a sparse line
// between restarts keeps a full-rate line behind it expired. Instead, packets
// that do not continue the playing line are tracked as an alternate line and
// dropped, and the alternate takes over only when the playing line stops, or
// the alternate clearly out-delivers it.
const (
	// lineAhead is the largest jump past a line's highest sequence number that
	// still continues the line. A larger jump starts another line. At 20ms per
	// packet, this lets the line absorb ~1s of loss.
	lineAhead = 50
	// lineBehind is how far behind a packet is a late arrival on the line rather
	// than another line.
	lineBehind = 100
	// lineQuiet is how long the playing line must be silent before an alternate
	// line can take over on its first packet.
	lineQuiet = 60 * time.Millisecond
	// lineWindow is the window the two lines' packet counts are compared over.
	lineWindow = 300 * time.Millisecond
	// lineMinPackets is how many packets the alternate line needs in the window,
	// and it must also have twice the playing line's, to take over on rate.
	lineMinPackets = 5
	// lineParallel is how long an alternate line must have run alongside the
	// playing one to count as a parallel stream rather than its successor. A
	// parallel line does not take over on a short silence of the playing line,
	// and the rates are compared over lineParallel, so a loss burst on the
	// playing line shorter than about half of it does not hand over the call.
	lineParallel = time.Second
)

type seqLine struct {
	ok     bool
	high   uint16      // highest sequence number on the line
	first  time.Time   // arrival of the line's first packet
	lastAt time.Time   // arrival of the line's latest packet
	at     []time.Time // arrivals within lineWindow
}

func (l *seqLine) fits(sn uint16) bool {
	return l.ok && (sn-l.high <= lineAhead || l.high-sn <= lineBehind)
}

func (l *seqLine) add(sn uint16, at time.Time) {
	if !l.ok {
		l.first = at
	}
	if !l.ok || before(l.high, sn) {
		l.high = sn
	}
	l.ok = true
	l.lastAt = at
	l.at = append(l.at, at)
}

// count returns the line's packets within window of now, forgetting those
// older than lineParallel.
func (l *seqLine) count(now time.Time, window time.Duration) int {
	i := 0
	for i < len(l.at) && now.Sub(l.at[i]) > lineParallel {
		i++
	}
	n := copy(l.at, l.at[i:])
	l.at = l.at[:n]
	for i = 0; i < n && now.Sub(l.at[i]) > window; i++ {
	}
	return n - i
}

func (l *seqLine) reset() {
	*l = seqLine{at: l.at[:0]}
}

type lineVote struct {
	cur, alt seqLine
}

// acceptLine reports whether sn belongs to the playing sequence line, switching
// lines first if sn's line should take over. Caller must hold b.mu.
func (b *Buffer) acceptLine(sn uint16, at time.Time) bool {
	v := &b.lines
	if !v.cur.ok || v.cur.fits(sn) {
		v.cur.add(sn, at)
		return true
	}
	if !v.alt.fits(sn) {
		v.alt.reset()
	}
	v.alt.add(sn, at)

	parallel := v.cur.lastAt.Sub(v.alt.first) >= lineParallel
	quiet := !parallel && at.Sub(v.cur.lastAt) >= lineQuiet
	if !quiet {
		window := lineWindow
		if parallel {
			window = lineParallel
		}
		alt := v.alt.count(at, window)
		if alt < lineMinPackets || alt < 2*v.cur.count(at, window) {
			return false
		}
	}

	// A line ahead of the playing one is just a gap, which popReady counts as
	// loss. Anything else needs sequence tracking cleared so it is not expired.
	ahead := before(v.cur.high, sn) && withinRange(sn, v.cur.high)
	if !ahead {
		b.resetStream()
	}
	// The playing line going quiet and resuming further ahead is plain loss,
	// not another line.
	if !quiet || !ahead {
		b.stats.SequenceRestarts++
		b.notifyStats()
	}

	v.cur, v.alt = v.alt, v.cur
	v.alt.reset()
	return true
}
