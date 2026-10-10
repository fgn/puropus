// Copyright (c) 2006-2011, Skype Limited. All rights reserved.
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions
// are met:
// - Redistributions of source code must retain the above copyright notice,
// this list of conditions and the following disclaimer.
// - Redistributions in binary form must reproduce the above copyright
// notice, this list of conditions and the following disclaimer in the
// documentation and/or other materials provided with the distribution.
// - Neither the name of Internet Society, IETF or IETF Trust, nor the
// names of specific contributors, may be used to endorse or promote
// products derived from this software without specific prior written
// permission.
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
// AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
// IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
// ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT OWNER OR CONTRIBUTORS BE
// LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR
// CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
// SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS
// INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN
// CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
// ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
// POSSIBILITY OF SUCH DAMAGE.
//
// Ported from libopus 1.5.2 silk/decode_pitch.c.

package silk

// decodePitch is silk_decode_pitch: the pitch lag of each subframe from the
// lag index and the contour index.
func decodePitch(lagIndex int16, contourIndex int8, pitchLags []int, fsKHz, nbSubfr int) {
	var (
		lagCBPtr []int8
		cbkSize  int
	)
	if fsKHz == 8 {
		if nbSubfr == peMaxNbSubfr {
			lagCBPtr = cbLagsStage2[:]
			cbkSize = peNbCbksStage2Ext
		} else {
			lagCBPtr = cbLagsStage2_10ms[:]
			cbkSize = peNbCbksStage2_10ms
		}
	} else {
		if nbSubfr == peMaxNbSubfr {
			lagCBPtr = cbLagsStage3[:]
			cbkSize = peNbCbksStage3Max
		} else {
			lagCBPtr = cbLagsStage3_10ms[:]
			cbkSize = peNbCbksStage3_10ms
		}
	}

	minLag := peMinLagMs * fsKHz
	maxLag := peMaxLagMs * fsKHz
	lag := minLag + int(lagIndex)

	for k := 0; k < nbSubfr; k++ {
		pitchLags[k] = lag + int(lagCBPtr[k*cbkSize+int(contourIndex)])
		pitchLags[k] = limit(pitchLags[k], minLag, maxLag)
	}
}
