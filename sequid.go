package mess

import (
	"math"
	"sort"

	"github.com/emersion/go-imap/v2"
)

var uselessSeq = imap.SeqRange{
	Start: math.MaxUint32,
	Stop:  math.MaxUint32,
}

var uselessUID = imap.UIDRange{
	Start: math.MaxUint32,
	Stop:  math.MaxUint32,
}

func uidToSeq(uidMap []imap.UID, initial imap.UIDRange) (imap.SeqRange, bool) {
	if len(uidMap) == 0 {
		return uselessSeq, false
	}

	seq := imap.SeqRange{}

	if initial.Start == 0 {
		seq.Start = uint32(len(uidMap))
	} else if initial.Start > uidMap[len(uidMap)-1] {
		return uselessSeq, false
	} else if initial.Start < uidMap[0] {
		seq.Start = 1
	} else {
		seq.Start = uint32(sort.Search(len(uidMap), func(i int) bool {
			return uidMap[i] >= initial.Start
		})) + 1
	}

	if seq.Start == math.MaxUint32 {
		return uselessSeq, false
	}

	if initial.Stop == 0 || initial.Stop > uidMap[len(uidMap)-1] {
		seq.Stop = uint32(len(uidMap))
	} else if initial.Stop < uidMap[0] {
		return uselessSeq, false
	} else {
		if initial.Start == initial.Stop {
			return imap.SeqRange{Start: seq.Start, Stop: seq.Start}, true
		}

		seq.Stop = uint32(sort.Search(len(uidMap), func(i int) bool {
			return uidMap[i] >= initial.Stop
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

func seqToUid(uidMap []imap.UID, initial imap.SeqRange) (imap.UIDRange, bool) {
	if len(uidMap) == 0 {
		return uselessUID, false
	}

	seq := imap.UIDRange{Start: imap.UID(initial.Start), Stop: imap.UID(initial.Stop)}
	start, stop := seq.Start, seq.Stop

	for {
		if start == 0 {
			seq.Start = uidMap[len(uidMap)-1]
		} else if start > imap.UID(len(uidMap)) {
			return uselessUID, false
		} else {
			seq.Start = uidMap[start-1]
		}

		if seq.Start != 0 {
			break
		}
		start++

		if initial.Start == initial.Stop {
			return uselessUID, false
		}
	}

	if initial.Start == initial.Stop {
		return imap.UIDRange{Start: seq.Start, Stop: seq.Start}, true
	}

	for {
		if stop == 0 || stop > imap.UID(len(uidMap)) {
			seq.Stop = uidMap[len(uidMap)-1]
		} else {
			seq.Stop = uidMap[stop-1]
		}

		if seq.Stop != 0 {
			break
		}
		stop--
		if stop == 0 {
			return uselessUID, false
		}
	}

	return seq, true
}
