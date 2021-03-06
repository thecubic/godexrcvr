package godexrcvr

import (
	"encoding/binary"
	"encoding/xml"
	"fmt"

	"github.com/google/gousb"

	"time"

	"github.com/snksoft/crc"
	"github.com/tarm/serial"
)

func DexcomFilter(desc *gousb.DeviceDesc) bool {
	switch desc.Vendor {
	case DexcomVendor:
		return desc.Product == Gen4ReceiverProduct
	default:
		return false
	}
}

func OpenDevice(device string) (*serial.Port, error) {
	sCfg := &serial.Config{Name: device, Baud: 115200, ReadTimeout: time.Second * 5}
	serDev, err := serial.OpenPort(sCfg)
	if err != nil {
		return nil, err
	}
	return serDev, nil
}

func checksumPacket(packet []byte) []byte {
	ckPacket := make([]byte, len(packet)+2)
	checksum := checksumForPacket(packet)
	copy(ckPacket, packet)
	binary.LittleEndian.PutUint16(ckPacket[len(packet):], checksum)
	return ckPacket
}

func checksumForPacket(packet []byte) uint16 {
	return uint16(crc.CalculateCRC(crc.XMODEM, packet))
}

func decodePacket(packet []byte) (*DexcomPacket, error) {
	if packet[0] != 0x01 {
		return nil, fmt.Errorf("packet does not start with sync byte")
	}
	pktFull := len(packet)
	packetLength := binary.LittleEndian.Uint16(packet[1:3])
	payloadLength := packetLength - 6
	packetChecksum := binary.LittleEndian.Uint16(packet[pktFull-2:])
	calcChecksum := checksumForPacket(packet[:len(packet)-2])
	if packetChecksum != calcChecksum {
		return nil, fmt.Errorf("checksum mismatch: %v [encoded], %v [calculated]", packetChecksum, calcChecksum)
	}
	payload := make([]byte, payloadLength)
	if payloadLength > 0 {
		copy(payload, packet[4:packetLength-2])
	}
	decodedPacket := &DexcomPacket{DexcomCmd(packet[3]), payload}
	return decodedPacket, nil
}

func buildCmdPacket(cmd DexcomCmd) []byte {
	var packet []byte
	packet = make([]byte, 6)
	packet[0] = 0x01
	binary.LittleEndian.PutUint16(packet[1:3], 6)
	packet[3] = byte(cmd)
	checksum := checksumForPacket(packet[:4])
	binary.LittleEndian.PutUint16(packet[4:6], checksum)
	return packet
}

func buildPacket(cmd DexcomCmd, payload []byte) []byte {
	var packet []byte
	payloadLength := uint16(len(payload))
	packetLength := payloadLength + 6
	packet = make([]byte, payloadLength+6)
	packet[0] = 0x01
	binary.LittleEndian.PutUint16(packet[1:3], packetLength)
	packet[3] = byte(cmd)
	checksum := checksumForPacket(packet[:packetLength-2])
	binary.LittleEndian.PutUint16(packet[packetLength-2:], checksum)
	return packet
}

func ReadPacket(device *serial.Port) (*DexcomPacket, error) {
	// this device is picky to read from.
	// instead of "give me what you got",
	// this will read the first 4 bytes
	// which is syncbyte packetlen cmd
	// and then tape-read the rest
	var err error
	var n int
	phdr := make([]byte, 4)
	n, err = device.Read(phdr)
	if err != nil {
		return nil, err
	}
	if n < 4 {
		return nil, fmt.Errorf("underflow")
	}
	if phdr[0] != 0x01 {
		return nil, fmt.Errorf("packet does not start with sync byte")
	}
	packetLength := binary.LittleEndian.Uint16(phdr[1:3])

	remaining := int(packetLength - uint16(n))
	toread := remaining

	fullPacket := make([]byte, 4+remaining)
	copy(fullPacket[0:4], phdr)

	rbuf := make([]byte, 128)
	offset := 4
	for toread > 0 {
		n, err = device.Read(rbuf)
		if err != nil {
			return nil, err
		}
		copy(fullPacket[offset:], rbuf)
		toread = toread - n
		offset = offset + n
	}

	return decodePacket(fullPacket)
}

func ReadBatteryLevel(device *serial.Port) (int, error) {
	packet := buildCmdPacket(CmdReadBatteryLevel)
	_, err := device.Write(packet)
	if err != nil {
		return -1, err
	}
	device.Flush()

	pkt, err := ReadPacket(device)
	if err != nil {
		return -1, err
	}
	if pkt.cmd != CmdAck {
		return -1, fmt.Errorf("command not acked")
	}
	return int(pkt.payload[0]), nil
}

func ReadFirmwareHeader(device *serial.Port) {
	packet := buildCmdPacket(CmdReadFirmwareHeader)
	_, err := device.Write(packet)
	if err != nil {
		fmt.Printf("error in writing: %v\n", err)
	}
	device.Flush()

	pkt, err := ReadPacket(device)
	if err != nil {
		panic(err)
	}
	if pkt.cmd != CmdAck {
		panic("command not acked")
	}
	fwHdr := new(FirmwareHeader)
	err = xml.Unmarshal(pkt.payload, &fwHdr)
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}
	fmt.Printf("SchemaVersion:      %v\n", fwHdr.SchemaVersion)
	fmt.Printf("ApiVersion:         %v\n", fwHdr.ApiVersion)
	fmt.Printf("TestApiVersion:     %v\n", fwHdr.TestApiVersion)
	fmt.Printf("ProductId:          %v\n", fwHdr.ProductId)
	fmt.Printf("ProductName:        %v\n", fwHdr.ProductName)
	fmt.Printf("SoftwareNumber:     %v\n", fwHdr.SoftwareNumber)
	fmt.Printf("FirmwareVersion:    %v\n", fwHdr.FirmwareVersion)
	fmt.Printf("PortVersion:        %v\n", fwHdr.PortVersion)
	fmt.Printf("RFVersion:          %v\n", fwHdr.RFVersion)
	fmt.Printf("BLESoftwareVersion: %v\n", fwHdr.BLESoftwareVersion)
	fmt.Printf("BLEHardwareVersion: %v\n", fwHdr.BLEHardwareVersion)
	fmt.Printf("BLEDeviceAddress:   %v\n", fwHdr.BLEDeviceAddress)
	fmt.Printf("DexBootVersion:     %v\n", fwHdr.DexBootVersion)
}

func DoAPing(device *serial.Port) bool {
	var err error
	var n int
	packet := buildCmdPacket(CmdPing)

	n, err = device.Write(packet)
	if err != nil {
		fmt.Printf("error in writing: %v\n", err)
	} else {
		fmt.Printf("nW: %v\n", n)
	}
	device.Flush()

	rbuf := make([]byte, 2560)
	n, err = device.Read(rbuf)
	if err != nil {
		fmt.Printf("error in reading: %v\n", err)
	} else {
		fmt.Printf("nR: %v\n", n)
		fmt.Printf("packet: %v\n", rbuf[:n])
	}
	ackPacket, err := decodePacket(rbuf[:n])
	return ackPacket.cmd == CmdAck
}

func ReadGeneric(device *serial.Port, cmd DexcomCmd) error {
	packet := buildCmdPacket(cmd)
	_, err := device.Write(packet)
	if err != nil {
		return err
	}
	device.Flush()

	pkt, err := ReadPacket(device)
	if err != nil {
		return err
	}
	if pkt.cmd != CmdAck {
		return fmt.Errorf("command not acked")
	}
	fmt.Printf("return payload (byte): %v\n", pkt.payload)
	fmt.Printf("return payload (str): %v\n", string(pkt.payload))
	return nil
}

func ReadTransmitterID(device *serial.Port) (string, error) {
	packet := buildCmdPacket(CmdReadTransmitterID)
	_, err := device.Write(packet)
	if err != nil {
		return "", err
	}
	device.Flush()

	pkt, err := ReadPacket(device)
	if err != nil {
		return "", err
	}
	if pkt.cmd != CmdAck {
		return "", fmt.Errorf("command not acked")
	}
	return string(pkt.payload), nil
}

func ReadDatabasePartionInfo(device *serial.Port) (*PartitionInfo, error) {
	packet := buildCmdPacket(CmdReadDatabasePartitionInfo)
	_, err := device.Write(packet)
	if err != nil {
		return nil, err
	}
	device.Flush()

	pkt, err := ReadPacket(device)
	if err != nil {
		return nil, err
	}
	if pkt.cmd != CmdAck {
		return nil, fmt.Errorf("command not acked")
	}
	partInfo := new(PartitionInfo)
	err = xml.Unmarshal(pkt.payload, &partInfo)
	if err != nil {
		fmt.Printf("couldn't unmarshal: %v\n", err)
		return nil, err
	}
	return partInfo, nil
}
