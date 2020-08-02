package sequpdate

import (
	"math"
	"sort"

	"github.com/emersion/go-imap"
)

var uselessSeq = imap.Seq{
	Start: math.MaxUint32,
	Stop: math.MaxUint32,
}

func uidToSeq(uidMap []uint32, seq imap.Seq) (imap.Seq, bool) {
	if len(uidMap) == 0 {
		return uselessSeq, false
	}

	initial := seq

	if seq.Start == 0 {
		seq.Start = uint32(len(uidMap))
	} else if seq.Start > uidMap[len(uidMap)-1] {
		return uselessSeq, false
	} else if seq.Start < uidMap[0] {
		seq.Start = 1
	} else {
		seq.Start = uint32(sort.Search(len(uidMap), func(i int) bool {
			return uidMap[i] >= seq.Start
		})) + 1
	}

	if seq.Start == math.MaxUint32 {
		return uselessSeq, false
	}

	if seq.Stop == 0 || seq.Stop > uidMap[len(uidMap)-1] {
		seq.Stop = uint32(len(uidMap))
	} else if seq.Stop < uidMap[0] {
		return uselessSeq, false
	} else {
		if initial.Start == initial.Stop {
			return imap.Seq{Start: seq.Start, Stop: seq.Start}, true
		}

		seq.Stop = uint32(sort.Search(len(uidMap), func(i int) bool {
			return uidMap[i] >= seq.Stop
		})) + 1
		if seq.Stop > uint32(len(uidMap)) || uidMap[seq.Stop-1] != initial.Stop {
			seq.Stop -= 1
		}
	}

	if seq.Start > seq.Stop || seq.Stop == math.MaxUint32 {
		return uselessSeq, false
	}

	return seq, true
}

func seqToUid(uidMap []uint32, seq imap.Seq) (imap.Seq, bool) {
	if len(uidMap) == 0 {
		return uselessSeq, false
	}

	initial := seq
	start, stop := seq.Start, seq.Stop

	for {
		if start == 0 {
			seq.Start = uidMap[len(uidMap)-1]
		} else if start > uint32(len(uidMap)) {
			return uselessSeq, false
		} else {
			seq.Start = uidMap[start-1]
		}

		if seq.Start != 0 {
			break
		}
		start++

		if initial.Start == initial.Stop {
			return uselessSeq, false
		}
	}

	if initial.Start == initial.Stop {
		return imap.Seq{Start: seq.Start, Stop: seq.Start}, true
	}

	for {
		if stop == 0 || stop > uint32(len(uidMap)) {
			seq.Stop = uidMap[len(uidMap)-1]
		} else {
			seq.Stop = uidMap[stop-1]
		}

		if seq.Stop != 0 {
			break
		}
		stop--
		if stop == 0 {
			return uselessSeq, false
		}
	}

	return seq, true
}