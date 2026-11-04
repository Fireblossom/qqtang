//go:build windows

// Run under Windows/Wine against one initialized reference client. The real
// QQTEncoder writes into scratch allocations only; no packet is dispatched.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"time"

	"qqtang/internal/tooling/winlaunch"
)

func main() {
	pid := flag.Uint("pid", 0, "initialized reference Client.exe Windows PID")
	address := flag.String("ip", "127.0.0.1", "advertised directory IPv4")
	port := flag.Uint("port", 58000, "advertised directory TCP port")
	output := flag.String("out", "", "JSON output file; stdout if empty")
	flag.Parse()
	ip, err := netip.ParseAddr(*address)
	if err != nil || !ip.Is4() || *port == 0 || *port > 65535 {
		fmt.Fprintln(os.Stderr, "requires IPv4 and a TCP port within1..65535")
		os.Exit(2)
	}
	if err := run(uint32(*pid), ip, uint16(*port), *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(pid uint32, ip netip.Addr, port uint16, output string) error {
	// NetCenter10004F6D queries this IID before invoking schema041E. Its
	// vtable+10 is encode; +14 is decode. Query through the actual registry.
	snapshot, err := winlaunch.QueryClientModuleInterfaceSnapshot(pid, "NetCenter.dll", 0x2F290, 8, 5*time.Second)
	if err != nil {
		return err
	}
	if !snapshot.Available {
		return fmt.Errorf("QQTEncoder interface is not initialized")
	}
	object, err := strconv.ParseUint(snapshot.Object, 0, 32)
	if err != nil {
		return err
	}
	// Independent native-object layout from QQTMsgData schema0806:
	// response10 + ten1570-byte KINGDOM_INFO records. Each kingdom carries
	// twenty74-byte DIR_SERVER_INFO records, whose optional DNS is empty.
	source := make([]byte, 15710)
	binary.LittleEndian.PutUint32(source[6:], 1)
	copy(source[10:50], []byte{0xB6, 0xAB, 0xB2, 0xBF, 0xB5, 0xE7, 0xD0, 0xC5})
	binary.LittleEndian.PutUint32(source[50:], 1)
	binary.LittleEndian.PutUint32(source[96:], 1)
	binary.LittleEndian.PutUint32(source[100:], 254)
	ipv4 := ip.As4()
	copy(source[146:150], ipv4[:])
	binary.LittleEndian.PutUint32(source[150:], 1)
	binary.LittleEndian.PutUint16(source[154:], port)
	encoded, err := winlaunch.ExportQQTEncoderMessageSource(pid, uintptr(object), 0x806, source, 5*time.Second)
	if err != nil {
		return err
	}
	decoded, err := winlaunch.ImportQQTEncoderMessage(pid, uintptr(object), 0x806, encoded.Payload, len(source), 5*time.Second)
	if err != nil {
		return err
	}
	if encoded.ReturnCode != 0 || decoded.ReturnCode != 0 || !bytes.Equal(source, decoded.Source) {
		return fmt.Errorf("original QQTEncoder did not round-trip the complete native object")
	}
	result := struct {
		Schema          uint32 `json:"schema"`
		NativeSize      int    `json:"native_size"`
		IP              string `json:"ip"`
		Port            uint16 `json:"port"`
		EncodedHex      string `json:"encoded_hex"`
		NativeRoundTrip bool   `json:"native_round_trip"`
	}{0x806, len(source), ip.String(), port, hex.EncodeToString(encoded.Payload), true}
	var writer = os.Stdout
	if output != "" {
		writer, err = os.Create(output)
		if err != nil {
			return err
		}
		defer writer.Close()
	}
	return json.NewEncoder(writer).Encode(result)
}
