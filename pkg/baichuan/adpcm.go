package baichuan

var imaIndexTable = []int{
	-1, -1, -1, -1, 2, 4, 6, 8,
	-1, -1, -1, -1, 2, 4, 6, 8,
}

var imaStepTable = []int{
	7, 8, 9, 10, 11, 12, 13, 14, 16, 17,
	19, 21, 23, 25, 28, 31, 34, 37, 41, 45,
	50, 55, 60, 66, 73, 80, 88, 97, 107, 118,
	130, 143, 157, 173, 190, 209, 230, 253, 279, 307,
	337, 371, 408, 449, 494, 544, 598, 658, 724, 796,
	876, 963, 1060, 1166, 1282, 1411, 1552, 1707, 1878, 2066,
	2272, 2499, 2749, 3024, 3327, 3660, 4026, 4428, 4871, 5358,
	5894, 6484, 7132, 7845, 8630, 9493, 10442, 11487, 12635, 13899,
	15289, 16818, 18500, 20350, 22385, 24623, 27086, 29794, 32767,
}

type ADPCMDecoder struct {
	predicted int
	index     int
}

func (d *ADPCMDecoder) Decode(data []byte) []int16 {
	out := make([]int16, len(data)*2)
	for i, b := range data {
		// First nibble (high 4 bits or low 4 bits? IMA usually low first or high first.
		// We will assume standard IMA: high nibble first, or DVI4 is low nibble first?
		// Let's decode both nibbles. Usually low nibble comes first in stream)
		nibbles := []byte{b & 0x0F, (b >> 4) & 0x0F}
		// DVI4 in RTP is high nibble first, but Microsoft WAV is low nibble first.
		// Let's assume low nibble first.
		for j, nibble := range nibbles {
			step := imaStepTable[d.index]
			diff := step >> 3
			if (nibble & 1) != 0 {
				diff += step >> 2
			}
			if (nibble & 2) != 0 {
				diff += step >> 1
			}
			if (nibble & 4) != 0 {
				diff += step
			}

			if (nibble & 8) != 0 {
				d.predicted -= diff
			} else {
				d.predicted += diff
			}

			if d.predicted > 32767 {
				d.predicted = 32767
			}
			if d.predicted < -32768 {
				d.predicted = -32768
			}

			d.index += imaIndexTable[nibble]
			if d.index < 0 {
				d.index = 0
			}
			if d.index > 88 {
				d.index = 88
			}

			out[i*2+j] = int16(d.predicted)
		}
	}
	return out
}
