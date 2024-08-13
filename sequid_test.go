package mess

import (
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestSeqToUid(t *testing.T) {
	uidMap := []imap.UID{2, 4, 6, 7, 8}
	test := func(seq imap.SeqRange, res imap.UIDRange, fail bool) {
		t.Helper()

		actualRes, ok := seqToUid(uidMap, seq)
		if !ok != fail {
			t.Errorf("%v => %v; fail: %v; ok: %v", seq, res, fail, ok)
			return
		}
		if !ok {
			return
		}
		if res.Start != actualRes.Start {
			t.Errorf("%v => %v; got %v", seq, res, actualRes)
			return
		}
		if res.Stop != actualRes.Stop {
			t.Errorf("%v => %v; got %v", seq, res, actualRes)
		}
	}

	test(imap.SeqRange{Start: 1}, imap.UIDRange{Start: 2, Stop: 8}, false)
	test(imap.SeqRange{}, imap.UIDRange{Start: 8, Stop: 8}, false)
	test(imap.SeqRange{Start: 1, Stop: 7}, imap.UIDRange{Start: 2, Stop: 8}, false)
	test(imap.SeqRange{Start: 1, Stop: 5}, imap.UIDRange{Start: 2, Stop: 8}, false)
	test(imap.SeqRange{Start: 1, Stop: 1}, imap.UIDRange{Start: 2, Stop: 2}, false)
	test(imap.SeqRange{Start: 5, Stop: 5}, imap.UIDRange{Start: 8, Stop: 8}, false)
	test(imap.SeqRange{Start: 2, Stop: 2}, imap.UIDRange{Start: 4, Stop: 4}, false)
	test(imap.SeqRange{Start: 2, Stop: 4}, imap.UIDRange{Start: 4, Stop: 7}, false)
	test(imap.SeqRange{Start: 6}, uselessUID, true)
	test(imap.SeqRange{Start: 6, Stop: 6}, uselessUID, true)

	uidMap = []imap.UID{}
	test(imap.SeqRange{Start: 1}, uselessUID, true)

	uidMap = []imap.UID{4}
	test(imap.SeqRange{Start: 1}, imap.UIDRange{Start: 4, Stop: 4}, false)

	uidMap = []imap.UID{2, 4, 0, 7, 8}
	test(imap.SeqRange{Start: 2, Stop: 3}, imap.UIDRange{Start: 4, Stop: 4}, false)
	test(imap.SeqRange{Start: 3, Stop: 3}, uselessUID, true)
}

func TestUidToSeq(t *testing.T) {
	uidMap := []imap.UID{2, 4, 6, 7, 8}
	test := func(seq imap.UIDRange, res imap.SeqRange, fail bool) {
		t.Helper()

		actualRes, ok := uidToSeq(uidMap, seq)
		if !ok != fail {
			t.Errorf("%v => %v; fail: %v; err: %v", seq, res, fail, ok)
			return
		}
		if !ok {
			return
		}
		if res.Start != actualRes.Start {
			t.Errorf("%v => %v; got %v", seq, res, actualRes)
			return
		}
		if res.Stop != actualRes.Stop {
			t.Errorf("%v => %v; got %v", seq, res, actualRes)
		}
	}

	test(imap.UIDRange{Start: 1}, imap.SeqRange{Start: 1, Stop: 5}, false)
	test(imap.UIDRange{Start: 1, Stop: 8}, imap.SeqRange{Start: 1, Stop: 5}, false)
	test(imap.UIDRange{Start: 2, Stop: 8}, imap.SeqRange{Start: 1, Stop: 5}, false)
	test(imap.UIDRange{Start: 2, Stop: 10}, imap.SeqRange{Start: 1, Stop: 5}, false)
	test(imap.UIDRange{Start: 2, Stop: 2}, imap.SeqRange{Start: 1, Stop: 1}, false)
	test(imap.UIDRange{}, imap.SeqRange{Start: 5, Stop: 5}, false)
	test(imap.UIDRange{Start: 8, Stop: 8}, imap.SeqRange{Start: 5, Stop: 5}, false)
	test(imap.UIDRange{Start: 3, Stop: 5}, imap.SeqRange{Start: 2, Stop: 2}, false)
	test(imap.UIDRange{Start: 9, Stop: 10}, uselessSeq, true)
	test(imap.UIDRange{Start: 1, Stop: 1}, uselessSeq, true)

	uidMap = []imap.UID{}
	test(imap.UIDRange{Start: 1}, uselessSeq, true)
	uidMap = []imap.UID{4}
	test(imap.UIDRange{Start: 4, Stop: 4}, imap.SeqRange{Start: 1, Stop: 1}, false)
}
