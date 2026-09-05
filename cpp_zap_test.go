package zip

// The C++ ZAP runtime is only worth generating if it writes the SAME BYTES the
// Go runtime writes. This test holds that directly: it emits the header,
// compiles a C++ driver against it, and compares the driver's buffers with
// buffers the Go runtime builds from the same calls in the same order.
//
// The two scripts below — cppZapScript and the Go cases in zapWireCases — are
// deliberately parallel, statement for statement. Keeping them in sync by hand
// is the point: when someone changes one side of the codec, the diff shows up
// here as a failing vector rather than as a fork on the wire.

import (
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	luxzap "github.com/luxfi/zap"
)

func fill(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed + byte(i*7)
	}
	return b
}

// zapWireCases is the Go half of the differential: one named buffer per case.
func zapWireCases() []struct {
	name  string
	bytes []byte
} {
	type kase = struct {
		name  string
		bytes []byte
	}
	var out []kase
	add := func(name string, f func() []byte) { out = append(out, kase{name, f()}) }

	add("scalar", func() []byte {
		b := luxzap.NewBuilder(256)
		ob := b.StartObject(8)
		ob.SetUint64(0, 0x0102030405060708)
		ob.FinishAsRoot()
		return b.Finish()
	})
	add("mixed", func() []byte {
		b := luxzap.NewBuilder(256)
		ob := b.StartObject(24)
		ob.SetUint8(0, 7)
		ob.SetUint16(2, 0xBEEF)
		ob.SetUint32(4, 0xDEADBEEF)
		ob.SetUint64(8, 0x1122334455667788)
		ob.SetBool(16, true)
		ob.SetBool(17, false)
		ob.FinishAsRoot()
		return b.Finish()
	})
	add("signed_and_float", func() []byte {
		b := luxzap.NewBuilder(256)
		ob := b.StartObject(32)
		ob.SetInt8(0, -3)
		ob.SetInt16(2, -300)
		ob.SetInt32(4, -70000)
		ob.SetInt64(8, -5000000000)
		ob.SetFloat32(16, -1.5)
		ob.SetFloat64(24, 3.141592653589793)
		ob.FinishAsRoot()
		return b.Finish()
	})
	add("bytes_tail", func() []byte {
		b := luxzap.NewBuilder(256)
		ob := b.StartObject(16)
		ob.SetBytes(0, []byte("hello"))
		ob.SetBytes(8, nil)
		ob.FinishAsRoot()
		return b.Finish()
	})
	add("two_tails_and_fixed", func() []byte {
		b := luxzap.NewBuilder(256)
		ob := b.StartObject(24)
		ob.SetBytes(0, fill(11, 0x40))
		ob.SetText(8, "zap")
		ob.SetBytesFixed(16, fill(8, 0x90))
		ob.FinishAsRoot()
		return b.Finish()
	})
	add("grow", func() []byte {
		b := luxzap.NewBuilder(64)
		ob := b.StartObject(16)
		ob.SetBytes(0, fill(1000, 0x11))
		ob.SetUint64(8, 0xFFFFFFFFFFFFFFFF)
		ob.FinishAsRoot()
		return b.Finish()
	})
	add("child_first", func() []byte {
		b := luxzap.NewBuilder(256)
		child := b.StartObject(8)
		child.SetUint64(0, 42)
		co := child.Finish()
		parent := b.StartObject(8)
		parent.SetObject(0, co)
		parent.SetUint32(4, 9)
		parent.FinishAsRoot()
		return b.Finish()
	})
	add("null_pointers", func() []byte {
		b := luxzap.NewBuilder(256)
		ob := b.StartObject(24)
		ob.SetObject(0, 0)
		ob.SetList(4, 0, 0)
		ob.SetList(12, 64, 0)
		ob.SetBytes(16, nil)
		ob.FinishAsRoot()
		return b.Finish()
	})
	add("list_u8", func() []byte {
		b := luxzap.NewBuilder(256)
		lb := b.StartList(1)
		for i := 0; i < 5; i++ {
			lb.AddUint8(byte(i + 1))
		}
		off, n := lb.Finish()
		ob := b.StartObject(8)
		ob.SetList(0, off, n)
		ob.FinishAsRoot()
		return b.Finish()
	})
	add("list_u32_u64", func() []byte {
		b := luxzap.NewBuilder(256)
		l1 := b.StartList(4)
		l1.AddUint32(1)
		l1.AddUint32(0xFFFFFFFF)
		o1, n1 := l1.Finish()
		l2 := b.StartList(8)
		l2.AddUint64(0x0102030405060708)
		l2.AddUint64(7)
		l2.AddUint64(0)
		o2, n2 := l2.Finish()
		ob := b.StartObject(16)
		ob.SetList(0, o1, n1)
		ob.SetList(8, o2, n2)
		ob.FinishAsRoot()
		return b.Finish()
	})
	add("list_bytes", func() []byte {
		b := luxzap.NewBuilder(256)
		lb := b.StartList(1)
		lb.AddBytes(fill(9, 0x21))
		lb.AddBytes(fill(4, 0x55))
		off, n := lb.Finish()
		ob := b.StartObject(8)
		ob.SetList(0, off, n)
		ob.FinishAsRoot()
		return b.Finish()
	})
	add("list_object_ptr", func() []byte {
		b := luxzap.NewBuilder(256)
		var pos []int
		for i := 0; i < 3; i++ {
			ob := b.StartObject(16)
			ob.SetUint64(0, uint64(i+1))
			ob.SetBytes(8, fill(3+i, byte(0x30+i)))
			pos = append(pos, ob.Finish())
		}
		lb := b.StartList(4)
		lb.AddObjectPtr(pos[0])
		lb.AddObjectPtr(0)
		lb.AddObjectPtr(pos[1])
		lb.AddObjectPtr(pos[2])
		off, n := lb.Finish()
		root := b.StartObject(8)
		root.SetList(0, off, n)
		root.FinishAsRoot()
		return b.Finish()
	})
	add("write_bytes_alignment", func() []byte {
		b := luxzap.NewBuilder(256)
		o1 := b.WriteBytes(fill(3, 0x01))
		o2 := b.WriteText("abcde")
		ob := b.StartObject(16)
		ob.SetList(0, o1, 3)
		ob.SetList(8, o2, 5)
		ob.FinishAsRoot()
		return b.Finish()
	})
	add("reserve_fixed", func() []byte {
		b := luxzap.NewBuilder(256)
		ob := b.StartObject(4)
		ob.SetUint32(0, 0x0A0B0C0D)
		ob.ReserveFixed(32)
		ob.SetUint64(24, 5)
		ob.FinishAsRoot()
		return b.Finish()
	})
	add("flags", func() []byte {
		b := luxzap.NewBuilder(256)
		ob := b.StartObject(8)
		ob.SetUint64(0, 1)
		ob.FinishAsRoot()
		return b.FinishWithFlags(luxzap.FlagSigned | luxzap.FlagCompressed)
	})
	add("version1", func() []byte {
		b := luxzap.NewBuilderV1(256)
		ob := b.StartObject(8)
		ob.SetUint64(0, 0x0908070605040302)
		ob.FinishAsRoot()
		return b.Finish()
	})
	add("no_root", func() []byte {
		b := luxzap.NewBuilder(256)
		ob := b.StartObject(8)
		ob.SetUint64(0, 3)
		ob.Finish()
		return b.Finish()
	})
	return out
}

// cppZapScript is the C++ half: the same calls, in the same order, against the
// emitted header.
const cppZapScript = `
#include "zap.hpp"

#include <cstdio>
#include <span>
#include <string>
#include <vector>

using namespace lux::zap;

static std::vector<std::uint8_t> fill(int n, std::uint8_t seed) {
    std::vector<std::uint8_t> v(std::size_t(n), std::uint8_t(0));
    for (int i = 0; i < n; ++i) v[std::size_t(i)] = std::uint8_t(seed + std::uint8_t(i * 7));
    return v;
}

static std::span<const std::uint8_t> sp(const std::vector<std::uint8_t>& v) { return {v.data(), v.size()}; }

static void emit(const char* name, const std::vector<std::uint8_t>& b) {
    static const char* d = "0123456789abcdef";
    std::string h;
    h.reserve(b.size() * 2);
    for (std::uint8_t c : b) { h.push_back(d[c >> 4]); h.push_back(d[c & 0xF]); }
    std::printf("%s\t%s\n", name, h.c_str());
}

int main() {
    {
        Builder b(256);
        auto ob = b.start_object(8);
        ob.set_u64(0, 0x0102030405060708ULL);
        ob.finish_as_root();
        emit("scalar", b.finish());
    }
    {
        Builder b(256);
        auto ob = b.start_object(24);
        ob.set_u8(0, 7);
        ob.set_u16(2, 0xBEEF);
        ob.set_u32(4, 0xDEADBEEF);
        ob.set_u64(8, 0x1122334455667788ULL);
        ob.set_bool(16, true);
        ob.set_bool(17, false);
        ob.finish_as_root();
        emit("mixed", b.finish());
    }
    {
        Builder b(256);
        auto ob = b.start_object(32);
        ob.set_i8(0, -3);
        ob.set_i16(2, -300);
        ob.set_i32(4, -70000);
        ob.set_i64(8, -5000000000LL);
        ob.set_f32(16, -1.5f);
        ob.set_f64(24, 3.141592653589793);
        ob.finish_as_root();
        emit("signed_and_float", b.finish());
    }
    {
        Builder b(256);
        auto ob = b.start_object(16);
        const char* hello = "hello";
        ob.set_bytes(0, {reinterpret_cast<const std::uint8_t*>(hello), 5});
        ob.set_bytes(8, {});
        ob.finish_as_root();
        emit("bytes_tail", b.finish());
    }
    {
        Builder b(256);
        auto ob = b.start_object(24);
        auto a = fill(11, 0x40);
        auto c = fill(8, 0x90);
        ob.set_bytes(0, sp(a));
        ob.set_text(8, "zap");
        ob.set_bytes_fixed(16, sp(c));
        ob.finish_as_root();
        emit("two_tails_and_fixed", b.finish());
    }
    {
        Builder b(64);
        auto ob = b.start_object(16);
        auto a = fill(1000, 0x11);
        ob.set_bytes(0, sp(a));
        ob.set_u64(8, 0xFFFFFFFFFFFFFFFFULL);
        ob.finish_as_root();
        emit("grow", b.finish());
    }
    {
        Builder b(256);
        auto child = b.start_object(8);
        child.set_u64(0, 42);
        auto co = child.finish();
        auto parent = b.start_object(8);
        parent.set_object(0, co);
        parent.set_u32(4, 9);
        parent.finish_as_root();
        emit("child_first", b.finish());
    }
    {
        Builder b(256);
        auto ob = b.start_object(24);
        ob.set_object(0, 0);
        ob.set_list(4, 0, 0);
        ob.set_list(12, 64, 0);
        ob.set_bytes(16, {});
        ob.finish_as_root();
        emit("null_pointers", b.finish());
    }
    {
        Builder b(256);
        auto lb = b.start_list(1);
        for (int i = 0; i < 5; ++i) lb.add_u8(std::uint8_t(i + 1));
        auto [off, n] = lb.finish();
        auto ob = b.start_object(8);
        ob.set_list(0, off, n);
        ob.finish_as_root();
        emit("list_u8", b.finish());
    }
    {
        Builder b(256);
        auto l1 = b.start_list(4);
        l1.add_u32(1);
        l1.add_u32(0xFFFFFFFF);
        auto [o1, n1] = l1.finish();
        auto l2 = b.start_list(8);
        l2.add_u64(0x0102030405060708ULL);
        l2.add_u64(7);
        l2.add_u64(0);
        auto [o2, n2] = l2.finish();
        auto ob = b.start_object(16);
        ob.set_list(0, o1, n1);
        ob.set_list(8, o2, n2);
        ob.finish_as_root();
        emit("list_u32_u64", b.finish());
    }
    {
        Builder b(256);
        auto lb = b.start_list(1);
        auto a = fill(9, 0x21);
        auto c = fill(4, 0x55);
        lb.add_bytes(sp(a));
        lb.add_bytes(sp(c));
        auto [off, n] = lb.finish();
        auto ob = b.start_object(8);
        ob.set_list(0, off, n);
        ob.finish_as_root();
        emit("list_bytes", b.finish());
    }
    {
        Builder b(256);
        std::vector<std::int64_t> pos;
        std::vector<std::vector<std::uint8_t>> keep;
        for (int i = 0; i < 3; ++i) {
            auto ob = b.start_object(16);
            ob.set_u64(0, std::uint64_t(i + 1));
            keep.push_back(fill(3 + i, std::uint8_t(0x30 + i)));
            ob.set_bytes(8, sp(keep.back()));
            pos.push_back(ob.finish());
        }
        auto lb = b.start_list(4);
        lb.add_object_ptr(pos[0]);
        lb.add_object_ptr(0);
        lb.add_object_ptr(pos[1]);
        lb.add_object_ptr(pos[2]);
        auto [off, n] = lb.finish();
        auto root = b.start_object(8);
        root.set_list(0, off, n);
        root.finish_as_root();
        emit("list_object_ptr", b.finish());
    }
    {
        Builder b(256);
        auto a = fill(3, 0x01);
        auto o1 = b.write_bytes(sp(a));
        auto o2 = b.write_text("abcde");
        auto ob = b.start_object(16);
        ob.set_list(0, o1, 3);
        ob.set_list(8, o2, 5);
        ob.finish_as_root();
        emit("write_bytes_alignment", b.finish());
    }
    {
        Builder b(256);
        auto ob = b.start_object(4);
        ob.set_u32(0, 0x0A0B0C0D);
        ob.reserve_fixed(32);
        ob.set_u64(24, 5);
        ob.finish_as_root();
        emit("reserve_fixed", b.finish());
    }
    {
        Builder b(256);
        auto ob = b.start_object(8);
        ob.set_u64(0, 1);
        ob.finish_as_root();
        emit("flags", b.finish_with_flags(kFlagSigned | kFlagCompressed));
    }
    {
        Builder b(256, kVersion1);
        auto ob = b.start_object(8);
        ob.set_u64(0, 0x0908070605040302ULL);
        ob.finish_as_root();
        emit("version1", b.finish());
    }
    {
        Builder b(256);
        auto ob = b.start_object(8);
        ob.set_u64(0, 3);
        ob.finish();
        emit("no_root", b.finish());
    }
    return 0;
}
`

// TestCppZapKeepsTheWire is the promise the generator makes: what it emits
// writes the same bytes the Go runtime writes.
func TestCppZapKeepsTheWire(t *testing.T) {
	cxx := cppCompiler(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "zap.hpp"), CppZap("lux::zap"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "driver.cpp"), []byte(cppZapScript), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(dir, "driver")
	build := exec.Command(cxx, "-std=c++23", "-O1", "-Wall", "-Wextra", "-Werror",
		"-I", dir, "-o", bin, filepath.Join(dir, "driver.cpp"))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compiling the emitted header failed: %v\n%s", err, out)
	}

	out, err := exec.Command(bin).Output()
	if err != nil {
		t.Fatalf("running the C++ driver failed: %v", err)
	}

	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, hexed, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("driver printed an unparsable line: %q", line)
		}
		got[name] = hexed
	}

	cases := zapWireCases()
	if len(got) != len(cases) {
		t.Fatalf("driver emitted %d vectors, Go built %d", len(got), len(cases))
	}
	for _, c := range cases {
		want := hex.EncodeToString(c.bytes)
		if got[c.name] != want {
			t.Errorf("%s: the C++ runtime and the Go runtime disagree on the wire\n  go  %s\n  cpp %s",
				c.name, want, got[c.name])
		}
	}
}

// TestCppZapReadsWhatGoWrote closes the loop the other way: the emitted reader
// must answer the same values over a buffer the Go builder produced.
func TestCppZapReadsWhatGoWrote(t *testing.T) {
	cxx := cppCompiler(t)

	b := luxzap.NewBuilder(256)
	tail := fill(11, 0x40)
	child := b.StartObject(8)
	child.SetUint64(0, 0xCAFEBABEDEADBEEF)
	co := child.Finish()
	lb := b.StartList(4)
	lb.AddUint32(3)
	lb.AddUint32(0xFFFFFFFF)
	lo, ln := lb.Finish()
	ob := b.StartObject(48)
	ob.SetUint8(0, 9)
	ob.SetUint64(8, 0x1122334455667788)
	ob.SetBytes(16, tail)
	ob.SetText(24, "zap")
	ob.SetObject(32, co)
	ob.SetList(36, lo, ln)
	ob.SetBytesFixed(44, []byte{1, 2, 3, 4})
	ob.FinishAsRoot()
	msg := b.Finish()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "zap.hpp"), CppZap("lux::zap"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reader.cpp"), []byte(cppZapReadScript), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "reader")
	if out, err := exec.Command(cxx, "-std=c++23", "-O1", "-Wall", "-Wextra", "-Werror",
		"-I", dir, "-o", bin, filepath.Join(dir, "reader.cpp")).CombinedOutput(); err != nil {
		t.Fatalf("compiling the reader failed: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, hex.EncodeToString(msg)).Output()
	if err != nil {
		t.Fatalf("running the reader failed: %v", err)
	}

	// The same reads, answered by the Go runtime the buffer came from.
	gm, err := luxzap.Parse(msg)
	if err != nil {
		t.Fatal(err)
	}
	r := gm.Root()
	gl := r.ListStride(36, 4)
	u := func(v uint64) string { return strconv.FormatUint(v, 10) }
	want := strings.Join([]string{
		"version " + u(uint64(gm.Version())),
		"u8 " + u(uint64(r.Uint8(0))),
		"u64 " + u(r.Uint64(8)),
		"bytes " + hex.EncodeToString(r.Bytes(16)),
		"text " + r.Text(24),
		"child " + u(r.Object(32).Uint64(0)),
		"list " + u(uint64(gl.Len())) + " " + u(uint64(gl.Uint32(0))) + " " + u(uint64(gl.Uint32(1))),
		"fixed " + hex.EncodeToString(r.BytesFixedSlice(44, 4)),
	}, "\n") + "\n"
	if string(out) != want {
		t.Errorf("the emitted reader answered differently than the Go runtime\n  got  %q\n  want %q", out, want)
	}
}

const cppZapReadScript = `
#include "zap.hpp"

#include <cstdio>
#include <string>
#include <vector>

using namespace lux::zap;

static std::string hexed(std::span<const std::uint8_t> b) {
    static const char* d = "0123456789abcdef";
    std::string h;
    for (std::uint8_t c : b) { h.push_back(d[c >> 4]); h.push_back(d[c & 0xF]); }
    return h;
}

int main(int argc, char** argv) {
    if (argc < 2) return 2;
    std::vector<std::uint8_t> buf;
    auto nib = [](char c) { return c <= '9' ? c - '0' : (c | 32) - 'a' + 10; };
    for (const char* p = argv[1]; p[0] && p[1]; p += 2) {
        buf.push_back(std::uint8_t(nib(p[0]) * 16 + nib(p[1])));
    }
    Message m;
    std::string err;
    if (!Message::parse({buf.data(), buf.size()}, &m, &err)) {
        std::printf("parse failed: %s\n", err.c_str());
        return 1;
    }
    const auto r = m.root();
    const auto l = r.list_stride(36, 4);
    std::printf("version %u\n", unsigned(m.version()));
    std::printf("u8 %u\n", unsigned(r.u8(0)));
    std::printf("u64 %llu\n", static_cast<unsigned long long>(r.u64(8)));
    std::printf("bytes %s\n", hexed(r.bytes(16)).c_str());
    std::printf("text %.*s\n", int(r.text(24).size()), r.text(24).data());
    std::printf("child %llu\n", static_cast<unsigned long long>(r.object(32).u64(0)));
    std::printf("list %lld %u %u\n", static_cast<long long>(l.len()), l.u32(0), l.u32(1));
    std::printf("fixed %s\n", hexed(r.bytes_fixed_slice(44, 4)).c_str());
    return 0;
}
`

func cppCompiler(t *testing.T) string {
	t.Helper()
	for _, c := range []string{"g++", "clang++"} {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	t.Skip("no C++ compiler on this machine; the emitted runtime cannot be checked here")
	return ""
}
