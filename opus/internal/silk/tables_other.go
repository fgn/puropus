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
// Ported from libopus 1.5.2 silk/tables_other.c (the tables the decoder uses).

package silk

// Other entropy coding and quantization tables (tables_other.c).

var stereoPredQuantQ13 = [16]int16{
	-13732, -10050, -8266, -7526, -6500, -5000, -2950, -820, 820, 2950, 5000, 6500,
	7526, 8266, 10050, 13732,
}

var stereoPredJointICDF = [25]uint8{
	249, 247, 246, 245, 244, 234, 210, 202, 201, 200, 197, 174,
	82, 59, 56, 55, 54, 46, 22, 12, 11, 10, 9, 7,
	0,
}

var stereoOnlyCodeMidICDF = [2]uint8{
	64, 0,
}

var lbrrFlags2ICDF = [3]uint8{
	203, 150, 0,
}

var lbrrFlags3ICDF = [7]uint8{
	215, 195, 166, 125, 110, 82, 0,
}

var lsbICDF = [2]uint8{
	120, 0,
}

var ltpScaleICDF = [3]uint8{
	128, 64, 0,
}

var typeOffsetVADICDF = [4]uint8{
	232, 158, 10, 0,
}

var typeOffsetNoVADICDF = [2]uint8{
	230, 0,
}

var nlsfInterpolationFactorICDF = [5]uint8{
	243, 221, 192, 181, 0,
}

var ltpScalesTableQ14 = [3]int16{
	15565, 12288, 8192,
}

var uniform3ICDF = [3]uint8{
	171, 85, 0,
}

var uniform4ICDF = [4]uint8{
	192, 128, 64, 0,
}

var uniform5ICDF = [5]uint8{
	205, 154, 102, 51, 0,
}

var uniform6ICDF = [6]uint8{
	213, 171, 128, 85, 43, 0,
}

var uniform8ICDF = [8]uint8{
	224, 192, 160, 128, 96, 64, 32, 0,
}

var nlsfEXTICDF = [7]uint8{
	100, 40, 16, 7, 3, 1, 0,
}

// lbrrFlagsICDFPtr is silk_LBRR_flags_iCDF_ptr.
var lbrrFlagsICDFPtr = [2][]uint8{
	lbrrFlags2ICDF[:],
	lbrrFlags3ICDF[:],
}

// quantizationOffsetsQ10 is silk_Quantization_Offsets_Q10.
var quantizationOffsetsQ10 = [2][2]int16{
	{offsetUVLQ10, offsetUVHQ10}, {offsetVLQ10, offsetVHQ10},
}
