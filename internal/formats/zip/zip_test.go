// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package zip

import (
	stdzip "archive/zip"
	"bytes"
	"cmp"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
	"pxy.se/go/binate/internal/formats/archive"
)

// toolArchives are archives Info-ZIP's zip made of d/, mode 0750, d/f,
// 0640, holding "hello\n", l, a symlink to d/f, and big, 0644, of 2800
// bytes, all of mtime 1700000000, written in CET: "info" of d and l, with
// owners and extended times, "plain" of d/f with -X, without either, so
// with its DOS time alone, "bz" of d/f and big, which -Z bzip2 compresses,
// and "enc" of d/f and big, encrypted with -P. "years" holds empty files
// of mtime 2040-01-01 00:00:00 UTC, future, and 1969-12-31 23:59:59 UTC,
// past, whose DOS time zip records as 1980, which unzip gives it.
var toolArchives = map[string]string{
	"years": "504b03040a00000000000008217800000000000000000000000006001c006675747572655554090003807eaa83807eaa8375780b000104e803000004e8030000504b03040a00000000000000210000000000000000000000000004001c00706173745554090003ffffffffffffffff75780b000104e803000004e8030000504b01021e030a000000000000082178000000000000000000000000060018000000000000000000a481000000006675747572655554050003807eaa8375780b000104e803000004e8030000504b01021e030a000000000000002100000000000000000000000000040018000000000000000000a48140000000706173745554050003ffffffff75780b000104e803000004e8030000504b05060000000002000200960000007e0000000000",
	"info":  "504b03040a0000000000aab96e5700000000000000000000000002001c00642f555409000300f153650d66036b75780b000104e803000004e8030000504b03040a0000000000aab96e5720303a36060000000600000003001c00642f66555409000300f1536500f1536575780b000104e803000004e803000068656c6c6f0a504b03040a0000000000aab96e57ee465206030000000300000001001c006c555409000300f1536500f1536575780b000104e803000004e8030000642f66504b01021e030a0000000000aab96e57000000000000000000000000020018000000000000001000e84100000000642f555405000300f1536575780b000104e803000004e8030000504b01021e030a0000000000aab96e5720303a360600000006000000030018000000000001000000a0813c000000642f66555405000300f1536575780b000104e803000004e8030000504b01021e030a0000000000aab96e57ee4652060300000003000000010018000000000000000000ffa17f0000006c555405000300f1536575780b000104e803000004e8030000504b05060000000003000300d8000000bd0000000000",
	"plain": "504b03040a0000000000aab96e5720303a36060000000600000003000000642f6668656c6c6f0a504b01021e030a0000000000aab96e5720303a360600000006000000030000000000000001000000a08100000000642f66504b0506000000000100010031000000270000000000",
	"bz":    "504b03040a0000000000aab96e5720303a36060000000600000003001c00642f66555409000300f153650d66036b75780b000104e803000004e803000068656c6c6f0a504b03042e0000000c00aab96e575a55312339000000f00a000003001c00626967555409000300f1536500f1536575780b000104e803000004e8030000425a6836314159265359595450a000025791804000322104002000508600295034e045e08b6116045e08b022c08be2ee48a70a120b2a8a1400504b01021e030a0000000000aab96e5720303a360600000006000000030018000000000000000000a08100000000642f66555405000300f1536575780b000104e803000004e8030000504b01021e032e0000000c00aab96e575a55312339000000f00a0000030018000000000000000000a48143000000626967555405000300f1536575780b000104e803000004e8030000504b0506000000000200020092000000b90000000000",
	"enc":   "504b03040a0009000000aab96e5720303a36120000000600000003001c00642f66555409000300f153650d66036b75780b000104e803000004e8030000e71864ec9fbc0973b05f581a277219d20e9e504b070820303a361200000006000000504b0304140009000800aab96e575a5531232a000000f00a000003001c00626967555409000300f153651666036b75780b000104e803000004e803000026e9329dcbcd9d42071bb30bb033f7a942108349829f20b6918c0b0b3744d894175f7d862d9d793c72e4504b07085a5531232a000000f00a0000504b01021e030a0009000000aab96e5720303a361200000006000000030018000000000001000000a08100000000642f66555405000300f1536575780b000104e803000004e8030000504b01021e03140009000800aab96e575a5531232a000000f00a0000030018000000000001000000a4815f000000626967555405000300f1536575780b000104e803000004e8030000504b0506000000000200020092000000d60000000000",
}

func tool(t testing.TB, name string) []byte {
	t.Helper()
	b, err := hex.DecodeString(toolArchives[name])
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fileOf returns a file holding data, as an archive is given.
func fileOf(t *testing.T, data []byte) *os.File {
	t.Helper()
	path := t.TempDir() + "/a.zip"
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func newBlob(t testing.TB, dir string) *os.File {
	t.Helper()
	blob, err := archive.NewBlob(dir)
	if err != nil {
		t.Fatal(err)
	}
	return blob
}

func read(t *testing.T, data []byte) *archive.Root {
	t.Helper()
	r, err := Read(fileOf(t, data), newBlob(t, t.TempDir()), "a.zip", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func contents(t *testing.T, r *archive.Root, rel string) string {
	t.Helper()
	f, err := r.Open(rel, false)
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestRead checks an archive Info-ZIP made, with modes, owners and
// extended times, which are to the second.
func TestRead(t *testing.T) {
	r := read(t, tool(t, "info"))
	mtime := time.Unix(1700000000, 0)
	for _, tc := range []struct {
		rel    string
		mode   uint32
		target string
	}{
		{"d", unix.S_IFDIR | 0o750, ""},
		{"d/f", unix.S_IFREG | 0o640, ""},
		{"l", unix.S_IFLNK | 0o777, "d/f"},
	} {
		st, err := r.Stat(tc.rel)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode != tc.mode || st.Target != tc.target || st.Uid != 1000 || st.Gid != 1000 ||
			!st.Mtime.Equal(mtime) || st.MtimePrecision != time.Second {
			t.Errorf("%s: %+v", tc.rel, st)
		}
	}
	if got := contents(t, r, "d/f"); got != "hello\n" {
		t.Errorf("d/f holds %q", got)
	}
	if _, err := r.Open("l", false); !errors.Is(err, files.ErrNotRegular) {
		t.Errorf("opening l: %v", err)
	}
	if modes, owners := r.Unrecorded(); modes || owners {
		t.Errorf("unrecorded modes %t, owners %t", modes, owners)
	}
	if r.Archive() != "zip" {
		t.Errorf("format %q", r.Archive())
	}
}

// TestReadDOSTime checks an entry with its DOS time alone: in local time,
// to two seconds, and without an owner.
func TestReadDOSTime(t *testing.T) {
	r := read(t, tool(t, "plain"))
	st, err := r.Stat("d/f")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2023, 11, 14, 23, 13, 20, 0, time.Local); !st.Mtime.Equal(want) || st.MtimePrecision != 2*time.Second {
		t.Errorf("mtime %v to %v, want %v to 2s", st.Mtime, st.MtimePrecision, want)
	}
	if modes, owners := r.Unrecorded(); modes || !owners {
		t.Errorf("unrecorded modes %t, owners %t", modes, owners)
	}
}

// TestReadYears checks times zip recorded after 2038 and before 1970,
// which both have the top bit of their seconds set: the DOS time tells
// them apart.
func TestReadYears(t *testing.T) {
	r := read(t, tool(t, "years"))
	for rel, want := range map[string]time.Time{
		"future": time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC),
		"past":   time.Date(1980, 1, 1, 0, 0, 0, 0, time.Local),
	} {
		if st, err := r.Stat(rel); err != nil || !st.Mtime.Equal(want) {
			t.Errorf("%s: %v, %v, want %v", rel, st.Mtime, err, want)
		}
	}
}

// TestReadNTFSTime checks an mtime in NTFS's extra field, to 100
// nanoseconds, as Windows tools record it.
func TestReadNTFSTime(t *testing.T) {
	mtime := time.Unix(1700000000, 123456700)
	// 100 nanoseconds since 1601, as a FILETIME.
	ft := uint64(mtime.UnixNano()/100) + 116444736000000000
	extra := []byte{0x0a, 0, 32, 0, 0, 0, 0, 0, 1, 0, 24, 0}
	for range 3 {
		extra = binary.LittleEndian.AppendUint64(extra, ft)
	}
	h := unixHeader("f", 0o644)
	// A DOS time beside, with no extended time, as Windows tools write.
	//lint:ignore SA1019 only the DOS fields write a DOS time alone
	h.Modified, h.ModifiedDate, h.ModifiedTime, h.Extra = time.Time{}, 0x5b6e, 0xb9aa, extra
	r := read(t, zipOf(t, []stdzip.FileHeader{h}, nil))
	st, err := r.Stat("f")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Mtime.Equal(mtime) || st.MtimePrecision != 100*time.Nanosecond {
		t.Errorf("mtime %v to %v, want %v to 100ns", st.Mtime, st.MtimePrecision, mtime)
	}
}

// TestReadMtimeFields checks that an mtime comes from the extra field
// that records it most finely, whatever their order, then from another
// that records it, and only else from the DOS time, in local time: a
// field without an mtime, or too short to hold one, is passed over.
func TestReadMtimeFields(t *testing.T) {
	// Away from UTC, as a DOS time taken in the wrong zone would show.
	saved := time.Local
	time.Local = time.FixedZone("UTC-5", -5*60*60)
	defer func() { time.Local = saved }()

	exact := time.Unix(1700000000, 123456700)
	ft := uint64(exact.UnixNano()/100) + 116444736000000000
	ntfs := []byte{0x0a, 0, 32, 0, 0, 0, 0, 0, 1, 0, 24, 0}
	for range 3 {
		ntfs = binary.LittleEndian.AppendUint64(ntfs, ft)
	}
	ut := []byte{0x55, 0x54, 5, 0, 1, 0x00, 0xf1, 0x53, 0x65}        // 1700000000
	utNoMtime := []byte{0x55, 0x54, 5, 0, 2, 0x00, 0xf1, 0x53, 0x65} // an atime alone
	pkware := []byte{0x0d, 0, 12, 0, 0, 0, 0, 0, 0x00, 0xf1, 0x53, 0x65, 0xe8, 3, 0xe8, 3}
	shortUX := []byte{0x58, 0x55, 4, 0, 0x00, 0xf1, 0x53, 0x65}
	// 2023-11-14 12:00:00, as a DOS date and time.
	dosDate, dosTime := uint16((2023-1980)<<9|11<<5|14), uint16(12<<11)
	dos := time.Date(2023, 11, 14, 12, 0, 0, 0, time.Local)
	// 2038-02-01, a DOS time from which the top bit means after 2038.
	date2038 := uint16((2038-1980)<<9 | 2<<5 | 1)
	for _, tc := range []struct {
		name      string
		extra     []byte
		mtime     time.Time
		precision time.Duration
		dosDate   uint16
	}{
		{"NTFS after UT", slices.Concat(ut, ntfs), exact, 100 * time.Nanosecond, 0},
		{"NTFS before UT", slices.Concat(ntfs, ut), exact, 100 * time.Nanosecond, 0},
		{"PKWARE's Unix field", pkware, time.Unix(1700000000, 0), time.Second, 0},
		// A second before 1970, not in 2106, which unzip passes over for
		// the DOS time, or another field's.
		{"UT before 1970", []byte{0x55, 0x54, 5, 0, 1, 0xff, 0xff, 0xff, 0xff}, dos, 2 * time.Second, 0},
		{"PKWARE's Unix field before 1970", []byte{0x0d, 0, 8, 0, 0, 0, 0, 0, 0xff, 0xff, 0xff, 0xff}, dos, 2 * time.Second, 0},
		{"UT before 1970 beside PKWARE's", slices.Concat([]byte{0x55, 0x54, 5, 0, 1, 0xff, 0xff, 0xff, 0xff}, pkware), time.Unix(1700000000, 0), time.Second, 0},
		// Unsigned beside a DOS time from 2038.
		{"UT after 2038", []byte{0x55, 0x54, 5, 0, 1, 0, 0, 0, 0x80}, time.Unix(1<<31, 0), time.Second, date2038},
		{"PKWARE's Unix field after 2038", []byte{0x0d, 0, 8, 0, 0, 0, 0, 0, 0xff, 0xff, 0xff, 0xff}, time.Unix(1<<32-1, 0), time.Second, date2038},
		{"UT without an mtime", utNoMtime, dos, 2 * time.Second, 0},
		{"Info-ZIP's old Unix field cut short", shortUX, dos, 2 * time.Second, 0},
		{"none", nil, dos, 2 * time.Second, 0},
	} {
		h := unixHeader("f", 0o644)
		//lint:ignore SA1019 only the DOS fields write a DOS time alone
		h.Modified, h.ModifiedDate, h.ModifiedTime, h.Extra = time.Time{}, cmp.Or(tc.dosDate, dosDate), dosTime, tc.extra
		r := read(t, zipOf(t, []stdzip.FileHeader{h}, nil))
		st, err := r.Stat("f")
		if err != nil {
			t.Fatal(err)
		}
		if !st.Mtime.Equal(tc.mtime) || st.MtimePrecision != tc.precision {
			t.Errorf("%s: mtime %v to %v, want %v to %v", tc.name, st.Mtime, st.MtimePrecision, tc.mtime, tc.precision)
		}
	}
}

// TestReadNames checks names as unzip and Python's zipfile take them: as
// they are if flagged as UTF-8, from a Unicode path field made from them,
// as they are if made on Unix, as unzip extracts them there, and else in
// code page 437, as Python reads those made on DOS and Windows, though
// their bytes form UTF-8.
func TestReadNames(t *testing.T) {
	unicodePath := func(raw, name string) []byte {
		b := []byte{0x75, 0x70, byte(5 + len(name)), 0, 1}
		b = binary.LittleEndian.AppendUint32(b, crc32.ChecksumIEEE([]byte(raw)))
		return append(b, name...)
	}
	for _, tc := range []struct {
		name, raw string
		utf8, dos bool
		extra     []byte
	}{
		{"café.txt", "caf\x82.txt", false, true, nil},
		{"├⌐.txt", "é.txt", false, true, nil},
		{"café.txt", "café.txt", true, true, nil},
		{"café.txt", "café.txt", false, false, nil},
		{"caf\x82.txt", "caf\x82.txt", false, false, nil},
		{"naïve.txt", "na\x8bve.txt", false, false, unicodePath("na\x8bve.txt", "naïve.txt")},
		// A Unicode path made from another name is passed over.
		{"naïve.txt", "na\x8bve.txt", false, true, unicodePath("other", "wrong.txt")},
	} {
		h := unixHeader(tc.raw, 0o644)
		h.NonUTF8, h.Extra = !tc.utf8, tc.extra
		if tc.dos {
			h.CreatorVersion, h.ExternalAttrs = 0, 0
		}
		r := read(t, zipOf(t, []stdzip.FileHeader{h}, map[string]string{tc.raw: "x"}))
		if names, _ := r.ReadDir(""); !slices.Equal(names, []string{tc.name}) {
			t.Errorf("%q, UTF-8 %t, DOS %t, extra % x: %q, want %q", tc.raw, tc.utf8, tc.dos, tc.extra, names, tc.name)
		}
	}
}

// TestReadBackslashes checks that names made on MS-DOS or Windows FAT
// with "\" between their parts, and no "/", are put in directories, and
// one ending in it is a directory, as unzip extracts them, but no others.
func TestReadBackslashes(t *testing.T) {
	var hdrs []stdzip.FileHeader
	data := map[string]string{}
	for _, f := range []struct {
		raw     string
		creator uint16
	}{
		{`src\main.c`, creatorFAT},
		{`a/b\c`, creatorFAT},
		{`ntfs\f`, 11},
		{`unix\f`, creatorUnix},
		// A directory, as unzip makes it, with no attribute saying so.
		{`empty\`, creatorFAT},
	} {
		h := unixHeader(f.raw, 0o644)
		h.CreatorVersion = f.creator<<8 | 20
		if f.creator != creatorUnix {
			h.ExternalAttrs = 0
		}
		hdrs = append(hdrs, h)
		data[f.raw] = "x"
	}
	r := read(t, zipOf(t, hdrs, data))
	for _, rel := range []string{"src/main.c", `a/b\c`, `ntfs\f`, `unix\f`} {
		if got := contents(t, r, rel); got != "x" {
			t.Errorf("%s holds %q", rel, got)
		}
	}
	if names, _ := r.ReadDir(""); !slices.Equal(names, []string{"a", "empty", `ntfs\f`, "src", `unix\f`}) {
		t.Errorf("root holds %q", names)
	}
	if e, err := r.Entry("empty", false); err != nil || e.Mode != unix.S_IFDIR|0o755 {
		t.Errorf("empty: %+v, %v", e, err)
	}
}

// TestReadKeepsDirectories checks that a directory stays where a later
// file has its name, empty or not, as unzip fails to replace it.
func TestReadKeepsDirectories(t *testing.T) {
	r := read(t, zipOf(t, []stdzip.FileHeader{
		{Name: "a/"}, {Name: "a/b"}, {Name: "a"}, {Name: "e/"}, {Name: "e"},
	}, map[string]string{"a/b": "x", "a": "y", "e": "z"}))
	for _, rel := range []string{"a", "e"} {
		if e, err := r.Entry(rel, false); err != nil || e.Mode&unix.S_IFMT != unix.S_IFDIR {
			t.Errorf("%s: %+v, %v", rel, e, err)
		}
	}
	if got := contents(t, r, "a/b"); got != "x" {
		t.Errorf("a/b holds %q", got)
	}

	// The contents of a file left out aren't stored.
	big := strings.Repeat("y", 1<<20)
	blob := newBlob(t, t.TempDir())
	r, err := Read(fileOf(t, zipOf(t, []stdzip.FileHeader{{Name: "a/"}, {Name: "a/b"}, {Name: "a"}}, map[string]string{"a/b": "x", "a": big})), blob, "a.zip", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if info, err := blob.Stat(); err != nil || info.Size() != 1 {
		t.Errorf("blob of %v, %v, want 1 byte", info.Size(), err)
	}
}

// TestReadMethods checks files compressed by each method read, and those
// that can't be read: encrypted, or by a method not read, each kept with
// why, as unzip goes on.
func TestReadMethods(t *testing.T) {
	big := strings.Repeat("binate ", 400)
	r := read(t, tool(t, "bz"))
	if got := contents(t, r, "big"); got != big {
		t.Errorf("bzip2: big holds %d bytes", len(got))
	}
	r = read(t, tool(t, "enc"))
	for _, rel := range []string{"d/f", "big"} {
		if _, err := r.Open(rel, false); err == nil || !strings.Contains(err.Error(), "encrypted") {
			t.Errorf("encrypted %s: %v", rel, err)
		}
	}

	var buf bytes.Buffer
	zw := stdzip.NewWriter(&buf)
	zw.RegisterCompressor(93, func(w io.Writer) (io.WriteCloser, error) { return zstd.NewWriter(w) })
	// zstd's older method, which Python's zipfile reads too.
	zw.RegisterCompressor(20, func(w io.Writer) (io.WriteCloser, error) { return zstd.NewWriter(w) })
	// xz's writer writes its header at once, which zip's writer makes
	// before the entry's.
	zw.RegisterCompressor(95, func(w io.Writer) (io.WriteCloser, error) { return &lazyXZ{w: w}, nil })
	zw.RegisterCompressor(9, func(w io.Writer) (io.WriteCloser, error) { return nopCloser{w}, nil })
	for name, method := range map[string]uint16{"store": stdzip.Store, "deflate": stdzip.Deflate, "zstd": 93, "zstd, old": 20, "xz": 95, "deflate64": 9} {
		w, err := zw.CreateHeader(&stdzip.FileHeader{Name: name, Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, big); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	r = read(t, buf.Bytes())
	for _, name := range []string{"store", "deflate", "zstd", "zstd, old", "xz"} {
		if got := contents(t, r, name); got != big {
			t.Errorf("%s: %d bytes", name, len(got))
		}
	}
	if _, err := r.Open("deflate64", false); err == nil || err.Error() != "open a.zip:deflate64: compressed with deflate64, which binate can't read" {
		t.Errorf("deflate64: %v", err)
	}
}

// pythonLZMA is an archive Python's zipfile made with ZIP_LZMA of big,
// as in toolArchives, empty, empty, and link, a symlink to big. Its
// streams end in end markers.
const pythonLZMA = "504b03043f0002000e005889775d5a5531232c000000f00a000003000000626967090405005d0000800000311a4a1eb6872be0d0c0b2b3b64e6f56f00a8ffc1af8a6bd230448729ffffae72000504b03043f0002000e005889775d00000000130000000000000005000000656d707479090405005d000080000083fffbffffc0000000504b03043f0002000e000000210049e2fbd31600000003000000040000006c696e6b090405005d0000800000311a49247803fffff73c4000504b01023f033f0002000e005889775d5a5531232c000000f00a0000030000000000000000000000800100000000626967504b01023f033f0002000e005889775d00000000130000000000000005000000000000000000000080014d000000656d707479504b01023f033f0002000e000000210049e2fbd31600000003000000040000000000000000000000ffa1830000006c696e6b504b0506000000000300030096000000bb0000000000"

// lzmaZip returns an archive of a file, name, holding data, compressed
// with lzma, whose stream ends without an end marker, its zip header
// holding props as the size of the properties, and its checksum crc.
func lzmaZip(t *testing.T, name, data string, props uint16, crc uint32) []byte {
	t.Helper()
	var c bytes.Buffer
	w, err := lzma.WriterConfig{Size: int64(len(data)), SizeInHeader: true}.NewWriter(&c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	stream := c.Bytes()
	raw := binary.LittleEndian.AppendUint16([]byte{9, 20}, props)
	raw = append(append(raw, stream[:5]...), stream[lzma.HeaderLen:]...)
	var buf bytes.Buffer
	zw := stdzip.NewWriter(&buf)
	fw, err := zw.CreateRaw(&stdzip.FileHeader{
		Name: name, Method: 14, CRC32: crc,
		CompressedSize64: uint64(len(raw)), UncompressedSize64: uint64(len(data)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestReadLZMA checks that files and symlinks compressed with lzma are
// read whether their streams end in an end marker or by their sizes, and
// that a file with the wrong checksum or header is kept as damaged.
func TestReadLZMA(t *testing.T) {
	big := strings.Repeat("binate ", 400)
	b, err := hex.DecodeString(pythonLZMA)
	if err != nil {
		t.Fatal(err)
	}
	r := read(t, b)
	if got := contents(t, r, "big"); got != big {
		t.Errorf("python: big holds %d bytes", len(got))
	}
	if got := contents(t, r, "empty"); got != "" {
		t.Errorf("python: empty holds %q", got)
	}
	if e, err := r.Entry("link", false); err != nil || e.Mode&unix.S_IFMT != unix.S_IFLNK || e.Target != "big" {
		t.Errorf("python: link %+v, %v", e, err)
	}
	r = read(t, lzmaZip(t, "big", big, 5, crc32.ChecksumIEEE([]byte(big))))
	if got := contents(t, r, "big"); got != big {
		t.Errorf("no end marker: big holds %d bytes", len(got))
	}
	for _, tc := range []struct {
		name  string
		props uint16
		crc   uint32
		want  string
	}{
		{"checksum", 5, 1, "damaged: zip: checksum error"},
		{"header", 6, crc32.ChecksumIEEE([]byte(big)), "damaged: lzma properties of 6 bytes, not 5"},
	} {
		r := read(t, lzmaZip(t, "big", big, tc.props, tc.crc))
		if _, err := r.Open("big", false); err == nil || !strings.HasSuffix(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// lazyXZ is an xz writer made on the first write.
type lazyXZ struct {
	w  io.Writer
	xw *xz.Writer
}

func (l *lazyXZ) Write(b []byte) (int, error) {
	if l.xw == nil {
		xw, err := xz.NewWriter(l.w)
		if err != nil {
			return 0, err
		}
		l.xw = xw
	}
	return l.xw.Write(b)
}

func (l *lazyXZ) Close() error {
	if l.xw == nil {
		return nil
	}
	return l.xw.Close()
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// zipOf returns an archive of hdrs, each regular file with the contents
// in data by its name.
func zipOf(t testing.TB, hdrs []stdzip.FileHeader, data map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := stdzip.NewWriter(&buf)
	for _, h := range hdrs {
		w, err := zw.CreateHeader(&h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, data[h.Name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// unixHeader returns the header of name, of mode, as made on Unix.
func unixHeader(name string, mode os.FileMode) stdzip.FileHeader {
	h := stdzip.FileHeader{Name: name, Method: stdzip.Deflate, Modified: time.Unix(1700000000, 0)}
	h.SetMode(mode)
	return h
}

// TestReadNotUnix checks entries made elsewhere than on Unix, as on
// Windows, which record no mode: a directory's is 0755, a file's 0644.
func TestReadNotUnix(t *testing.T) {
	r := read(t, zipOf(t, []stdzip.FileHeader{
		{Name: "dir/", Modified: time.Unix(1700000000, 0)},
		{Name: "dir/f", Method: stdzip.Deflate, Modified: time.Unix(1700000000, 0)},
	}, map[string]string{"dir/f": "x"}))
	for rel, mode := range map[string]uint32{"dir": unix.S_IFDIR | 0o755, "dir/f": unix.S_IFREG | 0o644} {
		if st, err := r.Stat(rel); err != nil || st.Mode != mode {
			t.Errorf("%s: %+v, %v", rel, st, err)
		}
	}
	if modes, owners := r.Unrecorded(); !modes || !owners {
		t.Errorf("unrecorded modes %t, owners %t", modes, owners)
	}
}

// TestReadReplaces checks entries put as unzip extracts them: a later
// one in place of an earlier one, and a member beneath a symlink, which
// unzip makes only once all else is extracted, in a directory in its
// place.
func TestReadReplaces(t *testing.T) {
	r := read(t, zipOf(t, []stdzip.FileHeader{
		unixHeader("d/", os.ModeDir|0o755),
		unixHeader("f", 0o644),
		unixHeader("alias", os.ModeSymlink|0o777),
		unixHeader("alias/g", 0o644),
		unixHeader("f", 0o600),
		unixHeader("empty", os.ModeSymlink|0o777),
	}, map[string]string{"alias": "d", "alias/g": "g"}))
	if names, _ := r.ReadDir(""); !slices.Equal(names, []string{"alias", "d", "f"}) {
		t.Errorf("root holds %q", names)
	}
	if st, _ := r.Stat("f"); st.Mode != unix.S_IFREG|0o600 {
		t.Errorf("f: %+v", st)
	}
	if st, _ := r.Stat("alias"); st.Mode&unix.S_IFMT != unix.S_IFDIR {
		t.Errorf("alias: %+v", st)
	}
	if got := contents(t, r, "alias/g"); got != "g" {
		t.Errorf("alias/g holds %q", got)
	}
	if names, _ := r.ReadDir("d"); len(names) != 0 {
		t.Errorf("d holds %q", names)
	}
}

// TestReadOwners checks owners in Info-ZIP's new Unix extra field, of
// any size up to 8 bytes, and that one that isn't well formed is none.
func TestReadOwners(t *testing.T) {
	field := func(data ...byte) []byte {
		return append([]byte{0x75, 0x78, byte(len(data)), 0}, data...)
	}
	for _, tc := range []struct {
		extra    []byte
		uid, gid uint32
		ok       bool
	}{
		{field(1, 4, 0xe8, 3, 0, 0, 4, 0xe9, 3, 0, 0), 1000, 1001, true},
		{field(1, 2, 0xe8, 3, 1, 5), 1000, 5, true},
		{field(1, 8, 0, 0, 0, 0, 1, 0, 0, 0, 1, 0), 0, 0, false},
		{field(2, 1, 1, 1, 1), 0, 0, false},
		{field(1, 4, 1, 2), 0, 0, false},
	} {
		h := unixHeader("f", 0o644)
		h.Extra = tc.extra
		r := read(t, zipOf(t, []stdzip.FileHeader{h}, nil))
		st, _ := r.Stat("f")
		_, noOwners := r.Unrecorded()
		if noOwners == tc.ok || tc.ok && (st.Uid != tc.uid || st.Gid != tc.gid) {
			t.Errorf("% x: %d:%d, unrecorded %t", tc.extra, st.Uid, st.Gid, noOwners)
		}
	}
}

// TestReadPipe checks an archive read from a pipe, copied to the blob
// first, the copy then freed.
func TestReadPipe(t *testing.T) {
	data := tool(t, "bz")
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = pw.Write(data)
		_ = pw.Close()
	}()
	defer func() { _ = pr.Close() }()
	var p archive.Progress
	r, err := Read(pr, newBlob(t, t.TempDir()), "/dev/fd/3", &p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if got := contents(t, r, "big"); got != strings.Repeat("binate ", 400) {
		t.Errorf("big holds %d bytes", len(got))
	}
	if got := contents(t, r, "d/f"); got != "hello\n" {
		t.Errorf("d/f holds %q", got)
	}
	if p.Read() != int64(len(data)) || p.Entries() != 2 {
		t.Errorf("read %d of %d, %d entries", p.Read(), len(data), p.Entries())
	}
}

// TestReadProgress checks that a file's bytes are each counted once as
// read, though archive/zip reads some again, so that the bar ends at the
// archive's size.
func TestReadProgress(t *testing.T) {
	data := tool(t, "bz")
	path := filepath.Join(t.TempDir(), "a.zip")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var p archive.Progress
	r, err := Read(f, newBlob(t, t.TempDir()), path, &p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if p.Read() > int64(len(data)) || p.Read() < int64(len(data))*9/10 || p.Entries() != 2 {
		t.Errorf("read %d of %d, %d entries", p.Read(), len(data), p.Entries())
	}
}

// TestReadDamaged checks that a file whose contents don't match their
// checksum is kept with why, as the rest is read.
func TestReadDamaged(t *testing.T) {
	data := zipOf(t, []stdzip.FileHeader{
		{Name: "bad", Method: stdzip.Store},
		{Name: "good", Method: stdzip.Store},
	}, map[string]string{"bad": "contents", "good": "fine"})
	i := bytes.Index(data, []byte("contents"))
	data[i] = 'C'
	r := read(t, data)
	if _, err := r.Open("bad", false); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Errorf("bad: %v", err)
	}
	if got := contents(t, r, "good"); got != "fine" {
		t.Errorf("good holds %q", got)
	}
}

// TestReadZeroChecksum checks that a file recorded with a checksum of 0,
// and no data descriptor, which archive/zip takes for none, is checked
// against it, as unzip checks it, and an empty file, whose checksum is 0,
// is read.
func TestReadZeroChecksum(t *testing.T) {
	var buf bytes.Buffer
	zw := stdzip.NewWriter(&buf)
	for name, data := range map[string]string{"zero": "contents", "empty": ""} {
		w, err := zw.CreateRaw(&stdzip.FileHeader{
			Name: name, Method: stdzip.Store, CRC32: 0,
			CompressedSize64: uint64(len(data)), UncompressedSize64: uint64(len(data)),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	r := read(t, buf.Bytes())
	if _, err := r.Open("zero", false); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("zero: %v", err)
	}
	if got := contents(t, r, "empty"); got != "" {
		t.Errorf("empty holds %q", got)
	}
}

// TestReadOutside checks that an entry with ".." in its name is read
// without each "..", as unzip extracts it.
func TestReadOutside(t *testing.T) {
	r := read(t, zipOf(t, []stdzip.FileHeader{
		{Name: "sub/../one", Method: stdzip.Store},
		{Name: "a/../../c", Method: stdzip.Store},
	}, map[string]string{"sub/../one": "1", "a/../../c": "c"}))
	for rel, want := range map[string]string{"sub/one": "1", "a/c": "c"} {
		if got := contents(t, r, rel); got != want {
			t.Errorf("%s holds %q, want %q", rel, got, want)
		}
	}
}

func TestReadRefuses(t *testing.T) {
	info := tool(t, "info")
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"empty", nil, "empty, not a zip archive"},
		{"garbage", bytes.Repeat([]byte("not a zip "), 100), "not a zip archive"},
		{"truncated", info[:len(info)-30], "not a zip archive"},
	} {
		_, err := Read(fileOf(t, tc.data), newBlob(t, t.TempDir()), "a.zip", nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}

func FuzzRead(f *testing.F) {
	for name := range toolArchives {
		f.Add(tool(f, name))
	}
	if b, err := hex.DecodeString(pythonLZMA); err == nil {
		f.Add(b)
	}
	h := unixHeader("l", os.ModeSymlink|0o777)
	f.Add(zipOf(f, []stdzip.FileHeader{h, unixHeader("l/f", 0o644)}, map[string]string{"l": "d", "l/f": "x"}))
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		path := t.TempDir() + "/a.zip"
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		in, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = in.Close() }()
		blob := newBlob(t, dir)
		defer func() { _ = blob.Close() }()
		r, err := Read(in, blob, "a.zip", nil)
		if err != nil {
			return
		}
		var walk func(rel string)
		walk = func(rel string) {
			if _, err := r.Stat(rel); err != nil {
				return
			}
			if f, err := r.Open(rel, false); err == nil {
				_, _ = io.Copy(io.Discard, f)
				_ = f.Close()
			}
			names, _ := r.ReadDir(rel)
			for _, name := range names {
				if rel != "" {
					name = rel + "/" + name
				}
				walk(name)
			}
		}
		walk("")
	})
}

// An archive's start tells zip from what isn't, as one after a program
// that extracts it, which zip: still reads.
func TestDetect(t *testing.T) {
	one := zipOf(t, []stdzip.FileHeader{{Name: "f", Method: stdzip.Store}}, map[string]string{"f": "x"})
	for _, tc := range []struct {
		name string
		data []byte
		want bool
	}{
		{"zip", one, true},
		{"of nothing", zipOf(t, nil, nil), true},
		{"zip64 of nothing", emptyZip64, true},
		{"split into one part", append([]byte("PK\x07\x08"), one...), true},
		{"after a program", append([]byte("\x7fELF"), one...), false},
		{"PK", []byte("PK"), false},
		{"empty", nil, false},
	} {
		if got := Detect(bytes.NewReader(tc.data)); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}

// emptyZip64 is a zip64 archive of nothing: its directory's zip64 end,
// that end's locator, and the end, its counts and offsets left to zip64's.
var emptyZip64 = []byte("" +
	"PK\x06\x06" + "\x2c\x00\x00\x00\x00\x00\x00\x00" + "\x2d\x00\x2d\x00" + "\x00\x00\x00\x00\x00\x00\x00\x00" +
	"\x00\x00\x00\x00\x00\x00\x00\x00" + "\x00\x00\x00\x00\x00\x00\x00\x00" +
	"\x00\x00\x00\x00\x00\x00\x00\x00" + "\x00\x00\x00\x00\x00\x00\x00\x00" +
	"PK\x06\x07" + "\x00\x00\x00\x00" + "\x00\x00\x00\x00\x00\x00\x00\x00" + "\x01\x00\x00\x00" +
	"PK\x05\x06" + "\x00\x00\x00\x00" + "\xff\xff\xff\xff" + "\xff\xff\xff\xff" + "\xff\xff\xff\xff" + "\x00\x00")

// TestReadEmptyZip64 checks that a zip64 archive of nothing is read, as
// an empty tree.
func TestReadEmptyZip64(t *testing.T) {
	blob, err := archive.NewBlob(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := Read(bytes.NewReader(emptyZip64), blob, "a.zip", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if names, err := root.ReadDir(""); err != nil || len(names) != 0 {
		t.Errorf("read %v, %v", names, err)
	}
}
