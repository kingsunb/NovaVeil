package client

import (
	"encoding/binary"
	"net"
	"testing"

	utls "github.com/refraction-networking/utls"
)

// bunHelloRaw 让 uTLS 按 bunClientHello 本地 marshal 出 ClientHello 原始字节,
// 不发起任何网络连接。指纹字段一旦漂移(升级 Bun 版本/改动 spec), 本组测试即红,
// 防止 TLS 回归静默通过 go test。
func bunHelloRaw(t *testing.T) []byte {
	t.Helper()
	uconn := utls.UClient(&net.TCPConn{}, &utls.Config{ServerName: "opencode.ai"}, utls.HelloCustom)
	if err := uconn.ApplyPreset(bunClientHello()); err != nil {
		t.Fatalf("apply preset: %v", err)
	}
	if err := uconn.BuildHandshakeState(); err != nil {
		t.Fatalf("build handshake state: %v", err)
	}
	raw, err := uconn.HandshakeState.Hello.Marshal()
	if err != nil {
		t.Fatalf("marshal hello: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("empty ClientHello")
	}
	return raw
}

// parseHello 解析握手记录: 返回密码套件、压缩、扩展(type, body)列表。
func parseHello(t *testing.T, raw []byte) ([]uint16, []byte, [][2][]byte) {
	t.Helper()
	if raw[0] != 0x01 {
		t.Fatalf("not a ClientHello handshake: 0x%02x", raw[0])
	}
	length := int(raw[1])<<16 | int(raw[2])<<8 | int(raw[3])
	body := raw[4 : 4+length]
	off := 2 + 32
	sidLen := int(body[off])
	off += 1 + sidLen
	csLen := int(binary.BigEndian.Uint16(body[off:]))
	off += 2
	suites := make([]uint16, 0, csLen/2)
	for i := 0; i < csLen; i += 2 {
		suites = append(suites, binary.BigEndian.Uint16(body[off+i:]))
	}
	off += csLen
	cmLen := int(body[off])
	compression := body[off+1 : off+1+cmLen]
	off += 1 + cmLen
	extTotal := int(binary.BigEndian.Uint16(body[off:]))
	off += 2
	exts := make([][2][]byte, 0, 12)
	end := off + extTotal
	for off < end {
		et := binary.BigEndian.Uint16(body[off:])
		el := int(binary.BigEndian.Uint16(body[off+2:]))
		exts = append(exts, [2][]byte{{byte(et >> 8), byte(et)}, body[off+4 : off+4+el]})
		off += 4 + el
	}
	return suites, compression, exts
}

func findExt(t *testing.T, exts [][2][]byte, extType uint16) []byte {
	t.Helper()
	for _, e := range exts {
		if binary.BigEndian.Uint16(e[0]) == extType {
			return e[1]
		}
	}
	t.Fatalf("extension 0x%04x missing", extType)
	return nil
}

// goldenSigAlgs 是官方 Bun 客户端实抓的签名算法序列(2026-10-24 对齐,
// 含 Ed25519 与 P-521)。上游按该指纹识别非官方客户端, 改动即指纹回归。
var goldenSigAlgs = []utls.SignatureScheme{
	utls.ECDSAWithP256AndSHA256,
	utls.PSSWithSHA256,
	utls.PKCS1WithSHA256,
	utls.ECDSAWithP384AndSHA384,
	utls.PSSWithSHA384,
	utls.PKCS1WithSHA384,
	utls.PSSWithSHA512,
	utls.PKCS1WithSHA512,
	utls.Ed25519,
	utls.ECDSAWithP521AndSHA512,
	utls.PKCS1WithSHA1,
}

// goldenExtOrder 是 Bun fetch 的扩展顺序, 与真客户端实抓逐位一致。
var goldenExtOrder = []uint16{
	0x0000, // server_name
	0x0017, // extended_master_secret
	0xff01, // renegotiation_info
	0x000a, // supported_groups
	0x000b, // ec_point_formats
	0x0023, // session_ticket
	0x0010, // alpn
	0x0005, // status_request
	0x000d, // signature_algorithms
	0x0012, // sct
	0x0033, // key_share
	0x002d, // psk_key_exchange_modes
	0x002b, // supported_versions
}

func TestBunClientHelloExtensionOrder(t *testing.T) {
	_, _, exts := parseHello(t, bunHelloRaw(t))
	if len(exts) != len(goldenExtOrder) {
		t.Fatalf("extension count %d, want %d", len(exts), len(goldenExtOrder))
	}
	for i, e := range exts {
		got := binary.BigEndian.Uint16(e[0])
		if got != goldenExtOrder[i] {
			t.Fatalf("extension[%d] = 0x%04x, want 0x%04x", i, got, goldenExtOrder[i])
		}
	}
}

func TestBunClientHelloSignatureAlgorithms(t *testing.T) {
	_, _, exts := parseHello(t, bunHelloRaw(t))
	body := findExt(t, exts, 0x000d)
	if len(body) != 2+2*len(goldenSigAlgs) {
		t.Fatalf("sigalgs body %d bytes, want %d", len(body), 2+2*len(goldenSigAlgs))
	}
	for i, want := range goldenSigAlgs {
		got := binary.BigEndian.Uint16(body[2+2*i:])
		if got != uint16(want) {
			t.Fatalf("sigalgs[%d] = 0x%04x, want 0x%04x (Ed25519/P-521 缺失即上游 429 指纹墙回归)", i, got, want)
		}
	}
}

func TestBunClientHelloCipherSuitesAndCurves(t *testing.T) {
	suites, compression, exts := parseHello(t, bunHelloRaw(t))
	first := utls.TLS_AES_128_GCM_SHA256
	if suites[0] != first {
		t.Fatalf("first cipher = 0x%04x, want 0x%04x", suites[0], first)
	}
	if len(compression) != 1 || compression[0] != 0 {
		t.Fatalf("compression = %v, want [0]", compression)
	}
	groups := findExt(t, exts, 0x000a)
	if len(groups) < 4 {
		t.Fatalf("supported_groups too short: %v", groups)
	}
	firstGroup := binary.BigEndian.Uint16(groups[2:])
	if firstGroup != uint16(utls.X25519MLKEM768) {
		t.Fatalf("first group = 0x%04x, want X25519MLKEM768", firstGroup)
	}
	alpn := findExt(t, exts, 0x0010)
	if string(alpn[2:]) != "http/1.1" && string(alpn[3:]) != "http/1.1" {
		t.Fatalf("alpn = %x, want http/1.1 only", alpn)
	}
	versions := findExt(t, exts, 0x002b)
	if len(versions) != 5 || binary.BigEndian.Uint16(versions[1:]) != 0x0304 || binary.BigEndian.Uint16(versions[3:]) != 0x0303 {
		t.Fatalf("supported_versions = %v, want TLS1.3+TLS1.2 only", versions)
	}
}
