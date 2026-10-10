package silk

import (
	"slices"
	"sort"
	"strconv"
	"testing"
)

// stateField is a field of decoderState under the name silk_ref.c prints
// for the matching silk_decoder_state field.
type stateField struct {
	name string
	ptr  any // *int, *int32, *int16, *int8, or a slice of those
}

// stateFields lists the fields of st for loading and comparing states
// recorded by tools/libopus-ref/silk_ref.c ("plc"). The table pointers are
// handled separately.
func stateFields(st *decoderState) []stateField {
	r := &st.resamplerState
	x := &st.indices
	return []stateField{
		{"prev_gain_Q16", &st.prevGainQ16},
		{"exc_Q14", st.excQ14[:]},
		{"sLPC_Q14_buf", st.sLPCQ14Buf[:]},
		{"outBuf", st.outBuf[:]},
		{"lagPrev", &st.lagPrev},
		{"LastGainIndex", &st.lastGainIndex},
		{"fs_kHz", &st.fsKHz},
		{"fs_API_hz", &st.fsAPIHz},
		{"nb_subfr", &st.nbSubfr},
		{"frame_length", &st.frameLength},
		{"subfr_length", &st.subfrLength},
		{"ltp_mem_length", &st.ltpMemLength},
		{"LPC_order", &st.lpcOrder},
		{"prevNLSF_Q15", st.prevNLSFQ15[:]},
		{"first_frame_after_reset", &st.firstFrameAfterReset},
		{"nFramesDecoded", &st.nFramesDecoded},
		{"nFramesPerPacket", &st.nFramesPerPacket},
		{"ec_prevSignalType", &st.ecPrevSignalType},
		{"ec_prevLagIndex", &st.ecPrevLagIndex},
		{"VAD_flags", st.vadFlags[:]},
		{"LBRR_flag", &st.lbrrFlag},
		{"LBRR_flags", st.lbrrFlags[:]},
		{"resampler.sIIR", r.sIIR[:]},
		{"resampler.sFIR", r.sFIR[:]},
		{"resampler.delayBuf", r.delayBuf[:]},
		{"resampler.resampler_function", &r.resamplerFunction},
		{"resampler.batchSize", &r.batchSize},
		{"resampler.invRatio_Q16", &r.invRatioQ16},
		{"resampler.Fs_in_kHz", &r.fsInKHz},
		{"resampler.Fs_out_kHz", &r.fsOutKHz},
		{"resampler.inputDelay", &r.inputDelay},
		{"indices.GainsIndices", x.gainsIndices[:]},
		{"indices.LTPIndex", x.ltpIndex[:]},
		{"indices.NLSFIndices", x.nlsfIndices[:]},
		{"indices.lagIndex", &x.lagIndex},
		{"indices.contourIndex", &x.contourIndex},
		{"indices.signalType", &x.signalType},
		{"indices.quantOffsetType", &x.quantOffsetType},
		{"indices.NLSFInterpCoef_Q2", &x.nlsfInterpCoefQ2},
		{"indices.PERIndex", &x.perIndex},
		{"indices.LTP_scaleIndex", &x.ltpScaleIndex},
		{"indices.Seed", &x.seed},
		{"sCNG.CNG_exc_buf_Q14", st.sCNG.cngExcBufQ14[:]},
		{"sCNG.CNG_smth_NLSF_Q15", st.sCNG.cngSmthNLSFQ15[:]},
		{"sCNG.CNG_synth_state", st.sCNG.cngSynthState[:]},
		{"sCNG.CNG_smth_Gain_Q16", &st.sCNG.cngSmthGainQ16},
		{"sCNG.rand_seed", &st.sCNG.randSeed},
		{"sCNG.fs_kHz", &st.sCNG.fsKHz},
		{"lossCnt", &st.lossCnt},
		{"prevSignalType", &st.prevSignalType},
		{"sPLC.pitchL_Q8", &st.sPLC.pitchLQ8},
		{"sPLC.LTPCoef_Q14", st.sPLC.ltpCoefQ14[:]},
		{"sPLC.prevLPC_Q12", st.sPLC.prevLPCQ12[:]},
		{"sPLC.last_frame_lost", &st.sPLC.lastFrameLost},
		{"sPLC.rand_seed", &st.sPLC.randSeed},
		{"sPLC.randScale_Q14", &st.sPLC.randScaleQ14},
		{"sPLC.conc_energy", &st.sPLC.concEnergy},
		{"sPLC.conc_energy_shift", &st.sPLC.concEnergyShift},
		{"sPLC.prevLTP_scale_Q14", &st.sPLC.prevLTPScaleQ14},
		{"sPLC.prevGain_Q16", st.sPLC.prevGainQ16[:]},
		{"sPLC.fs_kHz", &st.sPLC.fsKHz},
		{"sPLC.nb_subfr", &st.sPLC.nbSubfr},
		{"sPLC.subfr_length", &st.sPLC.subfrLength},
	}
}

// The tables a channel state points at, numbered as silk_ref.c prints them.
var (
	lagTables     = map[int][]uint8{4: uniform4ICDF[:], 6: uniform6ICDF[:], 8: uniform8ICDF[:]}
	contourTables = map[int][]uint8{0: pitchContourICDF[:], 1: pitchContourNBICDF[:], 2: pitchContour10MsICDF[:], 3: pitchContour10MsNBICDF[:]}
	nlsfCBs       = map[int]*nlsfCB{0: &nlsfCBNBMB, 1: &nlsfCBWB}
)

func tableID(t []uint8, tables map[int][]uint8) int {
	for id, table := range tables {
		if len(t) > 0 && &t[0] == &table[0] {
			return id
		}
	}
	return -1
}

func setValues[T int | int32 | int16 | int8](dst []T, v []int) {
	for i := range dst {
		dst[i] = T(v[i])
	}
}

func getValues[T int | int32 | int16 | int8](src []T) []int {
	out := make([]int, len(src))
	for i, x := range src {
		out[i] = int(x)
	}
	return out
}

// loadState builds a channel state from recorded fields.
func loadState(t *testing.T, fields map[string][]int) decoderState {
	t.Helper()
	var st decoderState
	for _, f := range stateFields(&st) {
		v, ok := fields[f.name]
		if !ok {
			t.Fatalf("recorded state lacks %s", f.name)
		}
		switch p := f.ptr.(type) {
		case *int:
			*p = v[0]
		case *int32:
			*p = int32(v[0])
		case *int16:
			*p = int16(v[0])
		case *int8:
			*p = int8(v[0])
		case []int:
			setValues(p, v)
		case []int32:
			setValues(p, v)
		case []int16:
			setValues(p, v)
		case []int8:
			setValues(p, v)
		}
	}
	st.pitchLagLowBitsICDF = lagTables[fields["pitch_lag_low_bits_iCDF"][0]]
	st.pitchContourICDF = contourTables[fields["pitch_contour_iCDF"][0]]
	st.psNLSFCB = nlsfCBs[fields["psNLSF_CB"][0]]
	return st
}

// dumpState returns the fields of st as silk_ref.c prints them.
func dumpState(st *decoderState) map[string][]int {
	out := map[string][]int{}
	for _, f := range stateFields(st) {
		switch p := f.ptr.(type) {
		case *int:
			out[f.name] = []int{*p}
		case *int32:
			out[f.name] = []int{int(*p)}
		case *int16:
			out[f.name] = []int{int(*p)}
		case *int8:
			out[f.name] = []int{int(*p)}
		case []int:
			out[f.name] = getValues(p)
		case []int32:
			out[f.name] = getValues(p)
		case []int16:
			out[f.name] = getValues(p)
		case []int8:
			out[f.name] = getValues(p)
		}
	}
	out["pitch_lag_low_bits_iCDF"] = []int{tableID(st.pitchLagLowBitsICDF, lagTables)}
	out["pitch_contour_iCDF"] = []int{tableID(st.pitchContourICDF, contourTables)}
	nlsf := -1
	for id, cb := range nlsfCBs {
		if st.psNLSFCB == cb {
			nlsf = id
		}
	}
	out["psNLSF_CB"] = []int{nlsf}
	return out
}

// plcCase is one case of testdata/plc.txt.
type plcCase struct {
	states  map[int]map[string][]int // by step
	outputs map[int][]int16          // by step
}

func readPLCCases(t *testing.T) map[string]*plcCase {
	t.Helper()
	cases := map[string]*plcCase{}
	get := func(name string) *plcCase {
		c := cases[name]
		if c == nil {
			c = &plcCase{states: map[int]map[string][]int{}, outputs: map[int][]int16{}}
			cases[name] = c
		}
		return c
	}
	vectors := readVectors(t, "plc.txt")
	for _, v := range vectors["state"] {
		c := get(v.fields[0])
		step, err := strconv.Atoi(v.fields[1])
		if err != nil {
			t.Fatalf("line %d: %v", v.line, err)
		}
		field := v.fields[2]
		v.pos = 3
		if c.states[step] == nil {
			c.states[step] = map[string][]int{}
		}
		c.states[step][field] = v.ints(t, len(v.fields)-3)
	}
	for _, v := range vectors["plcout"] {
		c := get(v.fields[0])
		step, err := strconv.Atoi(v.fields[1])
		if err != nil {
			t.Fatalf("line %d: %v", v.line, err)
		}
		v.pos = 2
		n := v.next(t)
		c.outputs[step] = v.int16s(t, n)
		v.done(t)
	}
	return cases
}

// TestPLCRecordedState conceals four lost frames with silk_decode_frame's
// loss path (silk_PLC, silk_CNG, silk_PLC_glue_frames) from channel states
// libopus recorded while decoding SILK fixtures (after voiced, unvoiced and
// inactive frames of 10, 20 and 60 ms packets) and from reset states, and
// requires libopus's concealed samples and its whole channel state after
// them.
func TestPLCRecordedState(t *testing.T) {
	cases := readPLCCases(t)
	if len(cases) < 10 {
		t.Fatalf("%d recorded cases", len(cases))
	}
	names := make([]string, 0, len(cases))
	for name := range cases {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c := cases[name]
		t.Run(name, func(t *testing.T) {
			st := loadState(t, c.states[0])
			// The recorded state must survive the round trip.
			if !equalStates(t, dumpState(&st), c.states[0]) {
				t.Fatal("state does not round-trip")
			}
			var out [maxFrameLength]int16
			for step := 1; step <= 4; step++ {
				n := decodeFrame(&st, nil, out[:], FlagPacketLost, codeIndependently)
				if !slices.Equal(out[:n], c.outputs[step]) {
					t.Fatalf("lost frame %d: concealed samples differ from libopus", step)
				}
			}
			equalStates(t, dumpState(&st), c.states[4])
		})
	}
}

func equalStates(t *testing.T, got, want map[string][]int) bool {
	t.Helper()
	ok := true
	for name, w := range want {
		if !slices.Equal(got[name], w) {
			t.Errorf("%s = %v, libopus %v", name, got[name], w)
			ok = false
		}
	}
	return ok
}
